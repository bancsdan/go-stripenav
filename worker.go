package stripenav

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
)

// Defaults for the worker's pacing.
const (
	DefaultMaxSleep      = 10 * time.Second
	DefaultPollInterval  = 5 * time.Second
	DefaultLeaseDuration = 60 * time.Second
	DefaultBaseBackoff   = 30 * time.Second
	DefaultMaxBackoff    = 15 * time.Minute
	DefaultBatchLimit    = 50
	ReportingDeadline    = 24 * time.Hour
)

// MetricsRecorder receives per-submission outcomes and per-call latency.
// Implementations should be cheap (counter increments). Nil is treated as
// the noop recorder.
type MetricsRecorder interface {
	RecordSubmissionResult(status string)
	RecordLatency(op string, d time.Duration)
}

type noopMetrics struct{}

func (noopMetrics) RecordSubmissionResult(string)       {}
func (noopMetrics) RecordLatency(string, time.Duration) {}

// NAVClient is the subset of *nav.Client the worker depends on. Defining
// it as an interface keeps the worker easy to test with a fake.
type NAVClient interface {
	SubmitInvoice(ctx context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error)
	AnnulInvoice(ctx context.Context, ops []nav.AnnulmentOperation) (nav.SubmitResult, error)
	QueryTransactionStatus(ctx context.Context, transactionID string, returnOriginal bool) (schemas.QueryTransactionStatusResponse, error)
}

// Worker is the submission lifecycle driver.
//
// It does not poll the store on a fixed tick. Instead it waits on a
// wakeup signal (from the webhook handler, after a successful Put) or
// on a maxSleep timeout, then claims a batch from the store and
// spawns a per-row goroutine to drive each claimed submission through
// its state machine. Each goroutine renews its claim lease while
// processing, releases on exit, and is naturally safe across multiple
// worker replicas because the store mediates row ownership.
type Worker struct {
	store    SubmissionStore
	client   NAVClient
	supplier mapping.Supplier
	logger   *slog.Logger
	metrics  MetricsRecorder
	clock    func() time.Time
	rng      *rand.Rand
	rngMu    sync.Mutex

	claimerID     string
	maxSleep      time.Duration
	pollInterval  time.Duration
	leaseDuration time.Duration
	baseBackoff   time.Duration
	maxBackoff    time.Duration
	batchLimit    int

	wakeup       chan struct{}
	lifecycleWG  sync.WaitGroup

	// derive lets the test inject a custom payload derivation for an
	// event. In production it is nil and the worker reads the persisted
	// RawEvent directly.
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

	// ClaimerID identifies this worker for claim ownership. Defaults to
	// the process hostname; if that's empty, a random suffix is used.
	// In multi-replica deployments each replica MUST have a unique id.
	ClaimerID string

	// MaxSleep bounds how long the worker waits before re-scanning the
	// store. Wakeup signals from the handler can interrupt this sleep
	// for locally-arrived rows; the sleep catches rows arriving on
	// other replicas, retries, and deadline checks. Defaults to 10s.
	MaxSleep time.Duration

	// PollInterval is how long a lifecycle goroutine sleeps between
	// queryTransactionStatus calls while a submission is in flight on
	// NAV. Defaults to 5s.
	PollInterval time.Duration

	// LeaseDuration is the TTL on a claim. If a worker crashes mid-
	// processing, another worker will be able to take the row after
	// this many seconds. Defaults to 60s.
	LeaseDuration time.Duration

	// BaseBackoff is the first retry delay after a retriable NAV error.
	// Defaults to 30s.
	BaseBackoff time.Duration
	// MaxBackoff is the cap on retry delay. Defaults to 15m.
	MaxBackoff time.Duration
	// BatchLimit caps the number of rows ClaimBatch returns per scan.
	// Defaults to 50.
	BatchLimit int

	// Derive is optional. When nil, the worker reads RawEvent directly
	// (the handler persists a NAV InvoiceData / InvoiceAnnulment blob).
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
	if cfg.ClaimerID == "" {
		cfg.ClaimerID = defaultClaimerID()
	}
	if cfg.MaxSleep <= 0 {
		cfg.MaxSleep = DefaultMaxSleep
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = DefaultLeaseDuration
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
		store:         cfg.Store,
		client:        cfg.Client,
		supplier:      cfg.Supplier,
		logger:        cfg.Logger,
		metrics:       cfg.Metrics,
		clock:         cfg.Clock,
		rng:           rand.New(rand.NewSource(cfg.Clock().UnixNano())),
		claimerID:     cfg.ClaimerID,
		maxSleep:      cfg.MaxSleep,
		pollInterval:  cfg.PollInterval,
		leaseDuration: cfg.LeaseDuration,
		baseBackoff:   cfg.BaseBackoff,
		maxBackoff:    cfg.MaxBackoff,
		batchLimit:    cfg.BatchLimit,
		wakeup:        make(chan struct{}, 1),
		derive:        cfg.Derive,
	}, nil
}

// defaultClaimerID returns hostname + a small random suffix so each
// replica gets a unique id without explicit config.
func defaultClaimerID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "worker"
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var suffix [8]byte
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range suffix {
		suffix[i] = alphabet[r.Intn(len(alphabet))]
	}
	return host + "-" + string(suffix[:])
}

// Wakeup hints to the worker that fresh work is available. Non-blocking;
// if a wakeup is already pending, additional signals collapse into it.
func (w *Worker) Wakeup() {
	select {
	case w.wakeup <- struct{}{}:
	default:
	}
}

// ClaimerID returns the id this worker uses when claiming submissions.
func (w *Worker) ClaimerID() string { return w.claimerID }

// Run blocks until ctx is cancelled. It scans the store on every
// wakeup or maxSleep interval, claims a batch, and spawns a lifecycle
// goroutine for each claimed row. On exit, Run waits for all in-flight
// lifecycle goroutines to drain.
func (w *Worker) Run(ctx context.Context) error {
	defer w.lifecycleWG.Wait()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.wakeup:
		case <-time.After(w.maxSleep):
		}
		if err := w.claimAndProcess(ctx, &w.lifecycleWG); err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("stripenav: claim batch failed", "err", err)
		}
	}
}

// Tick is exported for tests. It claims a batch, spawns a lifecycle
// goroutine for each row, and BLOCKS until all spawned goroutines
// finish. Tests can call it to drive the worker synchronously.
func (w *Worker) Tick(ctx context.Context) error {
	var local sync.WaitGroup
	if err := w.claimAndProcess(ctx, &local); err != nil {
		return err
	}
	local.Wait()
	return nil
}

func (w *Worker) claimAndProcess(ctx context.Context, wg *sync.WaitGroup) error {
	claimed, err := w.store.ClaimBatch(ctx, w.claimerID, w.batchLimit, w.leaseDuration)
	if err != nil {
		return err
	}
	for _, sub := range claimed {
		wg.Add(1)
		go func(s Submission) {
			defer wg.Done()
			w.runLifecycle(ctx, s)
		}(sub)
	}
	return nil
}

// runLifecycle drives one claimed submission through its state machine.
// The goroutine holds the claim (renewed periodically) for as long as
// it's making progress. It exits — releasing the claim — when the row
// reaches a terminal state, when it needs to wait longer than a few
// polls (long retry backoff, parent dependency), or when ctx is
// cancelled. The next ClaimBatch invocation picks up rows whose
// NextAttemptAt has come back due.
func (w *Worker) runLifecycle(ctx context.Context, initial Submission) {
	defer func() {
		// Best-effort release using a fresh context — even on shutdown
		// we want to clear the claim so another replica can pick up.
		relCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = w.store.ReleaseClaim(relCtx, initial.EventID, w.claimerID)
	}()

	leaseCtx, cancelLease := context.WithCancel(ctx)
	defer cancelLease()
	go w.renewLease(leaseCtx, initial.EventID)

	sub := initial
	for {
		if ctx.Err() != nil {
			return
		}

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
		case StatusAccepted, StatusRejected, StatusAborted:
			return
		}

		// Re-read the row after acting on it.
		fresh, err := w.store.Get(ctx, sub.EventID)
		if err != nil {
			w.logger.Warn("stripenav: post-action get failed",
				"event_id", sub.EventID, "err", err)
			return
		}
		sub = fresh

		if sub.IsTerminal() {
			return
		}

		// If the next attempt is far enough away, release and exit;
		// the next ClaimBatch will pick this row up when due.
		wait := sub.NextAttemptAt.Sub(w.clock())
		if wait > w.pollInterval {
			return
		}
		if wait < 0 {
			wait = 0
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// renewLease periodically extends the claim while the lifecycle
// goroutine is processing. Stops when ctx is cancelled (lifecycle
// exits) or when the renewal fails (claim was stolen / row deleted).
func (w *Worker) renewLease(ctx context.Context, eventID string) {
	ticker := time.NewTicker(w.leaseDuration / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.RenewClaim(ctx, eventID, w.claimerID, w.leaseDuration); err != nil {
				if !errors.Is(err, context.Canceled) {
					w.logger.Warn("stripenav: lease renewal failed",
						"event_id", eventID, "err", err)
				}
				return
			}
		}
	}
}

// parentReady returns true if sub has no parent dependency, or if the
// parent has reached StatusAccepted. If the parent is still in flight,
// the child's NextAttemptAt is pushed out so a future ClaimBatch picks
// it up after the parent has had a chance to progress. If the parent
// has terminally failed, the child is also marked aborted.
func (w *Worker) parentReady(ctx context.Context, sub Submission) bool {
	if sub.ParentEventID == "" {
		return true
	}
	parent, err := w.store.Get(ctx, sub.ParentEventID)
	if err != nil {
		w.logger.Warn("stripenav: parent lookup failed; will retry",
			"event_id", sub.EventID, "parent_event_id", sub.ParentEventID, "err", err)
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.NextAttemptAt = w.clock().Add(w.maxSleep)
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
			s.NextAttemptAt = w.clock().Add(w.maxSleep)
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
		res   nav.SubmitResult
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
		s.NextAttemptAt = w.clock().Add(w.pollInterval)
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
	default:
		// RECEIVED, PROCESSING, SAVED, or "" (no results yet) — keep polling.
		_ = w.store.UpdateStatus(ctx, sub.EventID, func(s *Submission) error {
			s.NextAttemptAt = w.clock().Add(w.pollInterval)
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
// should send. With the handler-persists-only flow it runs on every
// submit attempt (initial and retry); the operation is recorded on the
// submission at handler time, the payload in RawEvent.
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
		op = "CREATE"
	}
	return op, sub.RawEvent, nil, nil
}

func xmlLooksLike(b []byte, root string) bool {
	type any struct {
		XMLName xml.Name
	}
	var a any
	if err := xml.Unmarshal(b, &a); err != nil {
		return false
	}
	return a.XMLName.Local == root
}
