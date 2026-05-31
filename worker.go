package stripenav

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
)

// Defaults for the worker's retry pacing. Picked per design D8.
const (
	DefaultTickInterval = 30 * time.Second
	DefaultBaseBackoff  = 30 * time.Second
	DefaultMaxBackoff   = 15 * time.Minute
	DefaultBatchLimit   = 50
	ReportingDeadline   = 24 * time.Hour
)

// MetricsRecorder receives per-submission outcomes and per-call latency.
// Implementations should be cheap (counter increments). Nil is treated as
// the noop recorder.
type MetricsRecorder interface {
	RecordSubmissionResult(status string)
	RecordLatency(op string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) RecordSubmissionResult(string)             {}
func (noopMetrics) RecordLatency(string, time.Duration)       {}

// NAVClient is the subset of *nav.Client the worker depends on. Defining
// it as an interface keeps the worker easy to test with a fake.
type NAVClient interface {
	SubmitInvoice(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error)
	AnnulInvoice(ctx context.Context, ops []nav.AnnulmentOperation) (nav.SubmitResult, error)
	QueryTransactionStatus(ctx context.Context, transactionID string, returnOriginal bool) (schemas.QueryTransactionStatusResponse, error)
}

// Worker drives the submission lifecycle: it ticks on a fixed interval,
// pulls pending submissions from the store, and progresses each through
// the state machine.
type Worker struct {
	store    SubmissionStore
	client   NAVClient
	supplier mapping.Supplier
	logger   *slog.Logger
	metrics  MetricsRecorder
	clock    func() time.Time
	rng      *rand.Rand
	rngMu    sync.Mutex

	tickInterval time.Duration
	baseBackoff  time.Duration
	maxBackoff   time.Duration
	batchLimit   int

	// derive lets the test inject a custom payload derivation for an
	// event. In production it is nil and the worker uses the default
	// JSON-event → mapping pipeline.
	derive deriveFn
}

type deriveFn func(eventID string, raw []byte) (operation string, invoiceData []byte, annulment []byte, err error)

// WorkerConfig groups all the worker knobs.
type WorkerConfig struct {
	Store    SubmissionStore
	Client   NAVClient
	Supplier mapping.Supplier
	Logger   *slog.Logger
	Metrics  MetricsRecorder
	Clock    func() time.Time

	TickInterval time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	BatchLimit   int

	// Derive is optional. When nil, the worker assumes the submission's
	// RawEvent is a NAV InvoiceData blob already (the handler path); in
	// practice the handler invokes Submit directly and never goes
	// through the worker for the first attempt, so Derive only fires on
	// retries where the original mapping output may need to be recreated.
	Derive deriveFn
}

// NewWorker validates cfg and returns a Worker.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.Store == nil {
		return nil, errors.New("stripenav: WorkerConfig.Store is required")
	}
	if cfg.Client == nil {
		return nil, errors.New("stripenav: WorkerConfig.Client is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = noopMetrics{}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = DefaultTickInterval
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = DefaultBaseBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultMaxBackoff
	}
	if cfg.BatchLimit <= 0 {
		cfg.BatchLimit = DefaultBatchLimit
	}
	return &Worker{
		store:        cfg.Store,
		client:       cfg.Client,
		supplier:     cfg.Supplier,
		logger:       cfg.Logger,
		metrics:      cfg.Metrics,
		clock:        cfg.Clock,
		rng:          rand.New(rand.NewSource(cfg.Clock().UnixNano())),
		tickInterval: cfg.TickInterval,
		baseBackoff:  cfg.BaseBackoff,
		maxBackoff:   cfg.MaxBackoff,
		batchLimit:   cfg.BatchLimit,
		derive:       cfg.Derive,
	}, nil
}

// Run blocks until ctx is cancelled, calling tick at the configured
// interval.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := w.tick(ctx); err != nil {
				w.logger.Error("stripenav: worker tick failed", "err", err)
			}
		}
	}
}

// Tick is exported for tests; it processes one batch immediately.
func (w *Worker) Tick(ctx context.Context) error { return w.tick(ctx) }

func (w *Worker) tick(ctx context.Context) error {
	pending, err := w.store.ListPending(ctx, w.clock(), w.batchLimit)
	if err != nil {
		return err
	}
	for _, sub := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.progress(ctx, sub)
	}
	return nil
}

func (w *Worker) progress(ctx context.Context, sub Submission) {
	// Deadline check first: don't waste API calls on submissions whose
	// 24-hour window has already passed.
	if !sub.IssuedAt.IsZero() && w.clock().Sub(sub.IssuedAt) > ReportingDeadline {
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.LastError = "24-hour NAV reporting deadline elapsed"
			return s.Transition(StatusAborted)
		})
		w.metrics.RecordSubmissionResult(string(StatusAborted))
		w.logger.Error("stripenav: submission deadline elapsed",
			"event_id", sub.EventID, "invoice_number", sub.InvoiceNumber)
		return
	}

	switch sub.Status {
	case StatusPending:
		if !w.parentReady(ctx, sub) {
			return
		}
		w.attemptSubmit(ctx, sub)
	case StatusSubmitted, StatusProcessing:
		w.pollStatus(ctx, sub)
	}
}

// parentReady returns true if sub has no parent dependency, or if the
// parent has reached StatusAccepted. If the parent is still in flight
// (pending/submitted/processing) the child is deferred to the next tick.
// If the parent has terminally failed (rejected/aborted) the child is
// also marked aborted — it can never legally proceed.
func (w *Worker) parentReady(ctx context.Context, sub Submission) bool {
	if sub.ParentEventID == "" {
		return true
	}
	parent, err := w.store.Get(ctx, sub.ParentEventID)
	if err != nil {
		w.logger.Warn("stripenav: parent lookup failed; will retry",
			"event_id", sub.EventID, "parent_event_id", sub.ParentEventID, "err", err)
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.NextAttemptAt = w.clock().Add(w.tickInterval)
			return nil
		})
		return false
	}
	switch parent.Status {
	case StatusAccepted:
		return true
	case StatusRejected, StatusAborted:
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.LastError = "parent submission " + parent.EventID + " terminally failed: " + parent.LastError
			return s.Transition(StatusAborted)
		})
		w.metrics.RecordSubmissionResult(string(StatusAborted))
		w.logger.Error("stripenav: child aborted because parent failed",
			"event_id", sub.EventID, "parent_event_id", parent.EventID, "parent_status", parent.Status)
		return false
	default:
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.NextAttemptAt = w.clock().Add(w.tickInterval)
			return nil
		})
		return false
	}
}

func (w *Worker) attemptSubmit(ctx context.Context, sub Submission) {
	start := w.clock()
	op, invoiceData, annulData, err := w.deriveForRetry(sub)
	if err != nil {
		w.markFailure(ctx, sub, err, false /* nonRetriable */)
		return
	}

	var (
		res nav.SubmitResult
		txErr error
	)
	switch sub.Kind {
	case KindAnnulment:
		res, txErr = w.client.AnnulInvoice(ctx, []nav.AnnulmentOperation{{InvoiceAnnulment: annulData}})
	default:
		res, txErr = w.client.SubmitInvoice(ctx, []nav.InvoiceOperation{{Operation: op, InvoiceData: invoiceData}})
	}
	w.metrics.RecordLatency("submit", w.clock().Sub(start))

	if txErr != nil {
		var navErr *nav.NAVError
		retriable := false
		if errors.As(txErr, &navErr) {
			retriable = navErr.IsRetriable()
		}
		w.markFailure(ctx, sub, txErr, retriable)
		return
	}

	_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
		s.TransactionID = res.TransactionID
		s.LastError = ""
		s.Attempts++
		s.NextAttemptAt = w.clock().Add(w.tickInterval)
		return s.Transition(StatusSubmitted)
	})
}

func (w *Worker) pollStatus(ctx context.Context, sub Submission) {
	if sub.TransactionID == "" {
		w.markFailure(ctx, sub, errors.New("submitted submission missing transactionId"), false)
		return
	}
	start := w.clock()
	resp, err := w.client.QueryTransactionStatus(ctx, sub.TransactionID, false)
	w.metrics.RecordLatency("status", w.clock().Sub(start))
	if err != nil {
		var navErr *nav.NAVError
		retriable := true
		if errors.As(err, &navErr) {
			retriable = navErr.IsRetriable()
		}
		w.markFailure(ctx, sub, err, retriable)
		return
	}

	overall := overallStatus(resp)
	switch overall {
	case "ABORTED":
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.LastError = "NAV reported ABORTED"
			return s.Transition(StatusRejected)
		})
		w.metrics.RecordSubmissionResult(string(StatusRejected))
	case "FINISHED", "DONE":
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.LastError = ""
			return s.Transition(StatusAccepted)
		})
		w.metrics.RecordSubmissionResult(string(StatusAccepted))
	case "":
		// nothing yet; reschedule
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.NextAttemptAt = w.clock().Add(w.tickInterval)
			return s.Transition(StatusProcessing)
		})
	default:
		// RECEIVED, PROCESSING, SAVED
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.NextAttemptAt = w.clock().Add(w.tickInterval)
			return s.Transition(StatusProcessing)
		})
	}
}

func overallStatus(resp schemas.QueryTransactionStatusResponse) string {
	prs := resp.ProcessingResults.ProcessingResult
	if len(prs) == 0 {
		return ""
	}
	allFinal := true
	for _, p := range prs {
		switch p.InvoiceStatus {
		case "ABORTED":
			return "ABORTED"
		case "FINISHED", "DONE":
			// ok
		default:
			allFinal = false
		}
	}
	if allFinal {
		return "FINISHED"
	}
	return prs[0].InvoiceStatus
}

func (w *Worker) markFailure(ctx context.Context, sub Submission, cause error, retriable bool) {
	_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
		s.Attempts++
		s.LastError = cause.Error()
		if !retriable {
			w.metrics.RecordSubmissionResult(string(StatusRejected))
			return s.Transition(StatusRejected)
		}
		s.NextAttemptAt = w.clock().Add(w.backoffFor(s.Attempts))
		return s.Transition(StatusPending)
	})
}

func (w *Worker) backoffFor(attempts int) time.Duration {
	base := float64(w.baseBackoff)
	max := float64(w.maxBackoff)
	if attempts <= 0 {
		attempts = 1
	}
	delay := base * powInt(2, attempts-1)
	if delay > max {
		delay = max
	}
	w.rngMu.Lock()
	jitter := 0.8 + w.rng.Float64()*0.4 // ±20%
	w.rngMu.Unlock()
	return time.Duration(delay * jitter)
}

func powInt(base float64, exp int) float64 {
	if exp <= 0 {
		return 1
	}
	r := base
	for i := 1; i < exp; i++ {
		r *= base
		if r > 1e18 {
			return r
		}
	}
	return r
}

// deriveForRetry returns the operation literal and payloads the worker
// should send for a submission. With the handler-persists-only flow it
// runs on every submit attempt (initial as well as retry); the
// operation is recorded on the submission at handler time, the payload
// in RawEvent.
func (w *Worker) deriveForRetry(sub Submission) (operation string, invoiceData []byte, annulment []byte, err error) {
	if w.derive != nil {
		return w.derive(sub.EventID, sub.RawEvent)
	}
	if !xmlLooksLike(sub.RawEvent, "InvoiceData") && !xmlLooksLike(sub.RawEvent, "InvoiceAnnulment") {
		return "", nil, nil, errors.New("stripenav: worker has no Derive hook and RawEvent is not a NAV payload")
	}
	if sub.Kind == KindAnnulment {
		return "", nil, sub.RawEvent, nil
	}
	op := sub.Operation
	if op == "" {
		op = "CREATE" // backstop for records persisted before Operation was a field
	}
	return op, sub.RawEvent, nil, nil
}

func xmlLooksLike(b []byte, root string) bool {
	type any struct {
		XMLName xml.Name
	}
	var a any
	if xml.Unmarshal(b, &a) != nil {
		return false
	}
	return a.XMLName.Local == root
}
