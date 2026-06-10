package stripenav

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bancsdan/go-stripenav/internal/invoicemap"
	"github.com/bancsdan/go-stripenav/internal/navclient"
	"github.com/bancsdan/go-stripenav/internal/storeinmem"
	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
	stripe "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

// Config wires the Stripe-side and NAV-side concerns into a single
// HTTP handler. All required fields are checked by Handler() at startup.
type Config struct {
	// StripeWebhookSecret is the signing secret of the Stripe webhook
	// endpoint this handler will be mounted on (whsec_…).
	StripeWebhookSecret string

	// NAV is the NAV client configuration. NAV.BaseURL must be set
	// explicitly to ProductionBaseURL or TestBaseURL.
	NAV nav.Config

	// Supplier identifies the merchant. Required.
	Supplier mapping.Supplier

	// Store persists submission state. When nil, the bridge falls back
	// to an internal in-memory store and logs a Warn — fine for local
	// development, NEVER for production (submissions vanish on
	// restart). Production callers MUST supply a durable
	// implementation (Postgres etc.).
	Store SubmissionStore

	// ExchangeRateProvider returns the foreign→HUF exchange rate for
	// the given currency code. The `at` argument is the invoice's issue
	// time (the document's tax point), not the processing time — so a
	// redelivered or delayed webhook resolves the same rate as a prompt
	// one. Required when invoices in non-HUF currencies are expected;
	// if nil, non-HUF invoices will fail mapping.
	ExchangeRateProvider func(ctx context.Context, currency string, at time.Time) (string, error)

	// Logger is optional; defaults to slog.Default().
	Logger *slog.Logger
	// Metrics is optional; defaults to noopMetrics.
	Metrics MetricsRecorder
	// Clock is optional; defaults to time.Now (overridable for tests).
	Clock func() time.Time
	// AcceptTimeout bounds the handler's mapping + persist work per
	// webhook delivery. The handler never calls NAV inline — that runs
	// in the worker — so this only needs to cover store I/O and XML
	// marshalling. Defaults to 5s.
	AcceptTimeout time.Duration
	// DisableWorker disables the background worker — useful in unit
	// tests where ticks would race with the test scenario.
	DisableWorker bool

	// WorkerMaxSleep bounds how long the background worker sleeps
	// between store scans. Wakeup signals from the webhook handler can
	// interrupt this sleep for locally-arrived rows; the sleep caps
	// catch retries, status polls, and rows arriving on other replicas.
	// Defaults to 10s. Leave at default for prod.
	WorkerMaxSleep time.Duration

	// WorkerPollInterval is how often a lifecycle goroutine re-queries
	// queryTransactionStatus while a submission is in flight on NAV.
	// Defaults to 5s.
	WorkerPollInterval time.Duration

	// WorkerLeaseDuration is the TTL on a claim. If a worker crashes
	// mid-processing, another replica can take the row after this long.
	// Defaults to 60s.
	WorkerLeaseDuration time.Duration

	// WorkerClaimerID identifies this replica when claiming rows.
	// Defaults to hostname + a random suffix. In multi-replica
	// deployments, leaving this unset is fine; the random suffix
	// makes collisions vanishingly unlikely.
	WorkerClaimerID string

	// navClient is an injection seam for unit tests. Production callers
	// never set this directly; the Handler constructor builds a real
	// *navclient.Client from the NAV config. Tests in this package can poke at
	// it via the package-private surface, and external test code should
	// reach for WithNAVClient instead.
	navClient NAVClient
}

// Option mutates a Config. Used as `stripenav.Handler(cfg, opts...)` to
// keep injection seams out of the main Config struct.
type Option func(*Config)

// WithNAVClient overrides the NAV client the handler will use. Intended
// for tests and for advanced users wrapping the client with middleware
// (retries, tracing, etc.).
func WithNAVClient(c NAVClient) Option {
	return func(cfg *Config) { cfg.navClient = c }
}

// EligibleEventTypes are the Stripe event types the handler reports to
// NAV. Anything outside this set is acknowledged with 200 OK and
// otherwise ignored.
//
// Every Stripe lifecycle event routes to a manageInvoice operation
// (CREATE / STORNO / MODIFY) — never to manageAnnulment. NAV ANNUL is
// reserved for out-of-band "the report itself was malformed" cases that
// Stripe webhooks can't observe; trigger it manually via
// (*BridgeHandler).AnnulInvoice.
var EligibleEventTypes = map[string]EventKind{
	"invoice.finalized":            KindInvoice, // → CREATE
	"invoice.voided":               KindInvoice, // → STORNO
	"invoice.marked_uncollectible": KindInvoice, // → STORNO
	"credit_note.created":          KindCreditNote,
	"credit_note.voided":           KindCreditNote,
}

// Handler builds the configured http.Handler.
func Handler(cfg Config, opts ...Option) (*BridgeHandler, error) {
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	if cfg.navClient == nil {
		client, err := navclient.NewClient(cfg.NAV)
		if err != nil {
			return nil, fmt.Errorf("stripenav: %w", err)
		}
		cfg.navClient = client
	}

	h := &BridgeHandler{cfg: cfg}
	if !cfg.DisableWorker {
		worker, err := NewWorker(WorkerConfig{
			Store:         cfg.Store,
			Client:        cfg.navClient,
			Logger:        cfg.Logger,
			Metrics:       cfg.Metrics,
			Clock:         cfg.Clock,
			ClaimerID:     cfg.WorkerClaimerID,
			MaxSleep:      cfg.WorkerMaxSleep,
			PollInterval:  cfg.WorkerPollInterval,
			LeaseDuration: cfg.WorkerLeaseDuration,
		})
		if err != nil {
			return nil, err
		}
		h.worker = worker
		ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel is retained on the handler and invoked by Shutdown
		h.cancel = cancel
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				cfg.Logger.Error("stripenav: worker exited", "err", err)
			}
		}()
	}
	return h, nil
}

func validateConfig(cfg *Config) error {
	if cfg.StripeWebhookSecret == "" {
		return errors.New("stripenav: Config.StripeWebhookSecret is required")
	}
	if cfg.Supplier.TaxNumber == "" {
		return errors.New("stripenav: Config.Supplier.TaxNumber is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Store == nil {
		// Fall back to an internal in-memory store so the bridge works
		// out of the box for local development. State is lost on
		// restart — production callers must supply a durable Store.
		// Warn loudly so anyone running silently in prod sees this.
		cfg.Logger.Warn("stripenav: no Store configured; using internal in-memory store — submissions will be LOST on restart, do not use in production")
		cfg.Store = storeinmem.New()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = noopMetrics{}
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.AcceptTimeout <= 0 {
		cfg.AcceptTimeout = 5 * time.Second
	}
	return nil
}

// BridgeHandler implements http.Handler.
type BridgeHandler struct {
	cfg    Config
	worker *Worker
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Shutdown stops the background worker. Safe to call multiple times.
func (h *BridgeHandler) Shutdown(ctx context.Context) error {
	if h.cancel != nil {
		h.cancel()
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// permanentError marks a processing failure that a Stripe redelivery
// cannot fix: undecodable payloads, unmappable invoices, configuration
// gaps. The handler ACKs these with 200 (retrying would loop forever);
// everything else — store I/O, exchange-rate lookups — is transient and
// answered with 503 so Stripe's retry schedule redelivers the event.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

func isPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// ServeHTTP handles a Stripe webhook delivery.
func (h *BridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	sigHeader := r.Header.Get("Stripe-Signature")
	if sigHeader == "" {
		h.cfg.Logger.Warn("stripenav: missing Stripe-Signature")
		http.Error(w, "missing signature", http.StatusBadRequest)
		return
	}
	event, err := webhook.ConstructEventWithOptions(body, sigHeader, h.cfg.StripeWebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		h.cfg.Logger.Warn("stripenav: signature verification failed", "err", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.AcceptTimeout)
	defer cancel()

	if err := h.processEvent(ctx, &event, body); err != nil {
		h.cfg.Logger.Error("stripenav: process event", "event_id", event.ID, "type", event.Type, "err", err)
		// Permanent failures (undecodable / unmappable event) get a 200:
		// Stripe redelivering the same payload would fail identically,
		// and a non-2xx would keep the delivery in Stripe's retry queue
		// for days. Transient failures (store down, rate lookup failed)
		// get a 503 — nothing was persisted, so Stripe's redelivery is
		// the only retry path for this event.
		if !isPermanent(err) {
			http.Error(w, "temporarily unable to accept event", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *BridgeHandler) processEvent(ctx context.Context, event *stripe.Event, rawBody []byte) error {
	kind, eligible := EligibleEventTypes[string(event.Type)]
	if !eligible {
		return nil
	}

	// Dedup: once an event id is in the store the bridge has accepted
	// it. The worker is responsible for driving it to terminal state.
	// Re-deliveries (Stripe retry, manual replay) should be no-ops.
	if _, err := h.cfg.Store.Get(ctx, event.ID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store.Get: %w", err)
	}

	now := h.cfg.Clock()
	sub := Submission{
		EventID:       event.ID,
		Kind:          kind,
		Status:        StatusPending,
		IssuedAt:      time.Unix(event.Created, 0).UTC(),
		CreatedAt:     now,
		UpdatedAt:     now,
		NextAttemptAt: now,
		RawEvent:      rawBody,
	}

	switch kind {
	case KindInvoice:
		return h.processInvoice(ctx, event, &sub)
	case KindCreditNote:
		return h.processCreditNote(ctx, event, &sub)
	}
	return nil
}

func (h *BridgeHandler) processInvoice(ctx context.Context, event *stripe.Event, sub *Submission) error {
	inv, err := decodeInvoice(event)
	if err != nil {
		return permanent(err)
	}

	var (
		invForMap *stripe.Invoice
		opts      invoicemap.MapOptions
		op        string
	)
	switch event.Type {
	case "invoice.finalized":
		invForMap = inv
		op = "CREATE"
		opts = invoicemap.MapOptions{
			Supplier:  h.cfg.Supplier,
			Operation: invoicemap.OpCreate,
		}
	case "invoice.voided", "invoice.marked_uncollectible":
		invForMap = invoiceAsStorno(inv, h.cfg.Clock())
		op = "STORNO"
		opts = invoicemap.MapOptions{
			Supplier:              h.cfg.Supplier,
			Operation:             invoicemap.OpStorno,
			OriginalInvoiceNumber: inv.Number,
		}
		// The storno's lines extend the original's chain positions; the
		// original is `inv` itself, pre-clone.
		if inv.Lines != nil {
			opts.OriginalLineCount = len(inv.Lines.Data)
		}
		// Record the dependency on the prior CREATE so the worker can
		// wait for NAV to finish processing it before submitting this
		// storno. NAV rejects stornos whose original is still in
		// PROCESSING / SAVED state.
		if parent := findParentSubmission(ctx, h.cfg.Store, inv.Number); parent != "" {
			sub.ParentEventID = parent
		}
	default:
		return permanent(fmt.Errorf("stripenav: unexpected invoice event %q", event.Type))
	}

	// The rate is anchored to the document's issue time: the original's
	// finalization for a CREATE, "now" for a storno (its own issuance
	// moment).
	rate, err := h.rateFor(ctx, string(inv.Currency), stripeInvoiceIssueTime(invForMap, h.cfg.Clock()))
	if err != nil {
		return err
	}
	opts.ExchangeRateToHUF = rate

	sub.InvoiceNumber = invForMap.Number

	mapped, err := invoicemap.MapInvoice(invForMap, opts)
	if err != nil {
		return permanent(err)
	}
	return h.persistMapped(ctx, sub, mapped, op)
}

// persistMapped marshals the mapped InvoiceData into the submission's
// RawEvent (so the worker can retry without re-mapping), persists it,
// and wakes the worker. A Put that reports ErrAlreadyExists is the
// benign side of the Get-then-Put dedup race — a concurrent delivery of
// the same event won — and is treated as success.
func (h *BridgeHandler) persistMapped(ctx context.Context, sub *Submission, mapped schemas.InvoiceData, op string) error {
	payload, err := xml.Marshal(mapped)
	if err != nil {
		return permanent(err)
	}
	sub.RawEvent = append([]byte(xml.Header), payload...)
	sub.Operation = op

	if err := h.cfg.Store.Put(ctx, *sub); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return nil
		}
		return err
	}
	if h.worker != nil {
		h.worker.Wakeup()
	}
	return nil
}

// stripeInvoiceIssueTime mirrors the mapper's issue-date derivation:
// finalization time, then effective_at, then created, then fallback.
func stripeInvoiceIssueTime(inv *stripe.Invoice, fallback time.Time) time.Time {
	if inv.StatusTransitions != nil && inv.StatusTransitions.FinalizedAt > 0 {
		return time.Unix(inv.StatusTransitions.FinalizedAt, 0).UTC()
	}
	if inv.EffectiveAt > 0 {
		return time.Unix(inv.EffectiveAt, 0).UTC()
	}
	if inv.Created > 0 {
		return time.Unix(inv.Created, 0).UTC()
	}
	return fallback
}

// findParentSubmission looks up a previously-recorded submission whose
// InvoiceNumber matches the given (original, un-suffixed) invoice
// number. Returns its EventID or "" if none exists / lookup fails. We
// never block dispatch on lookup errors — a missing parent just means
// "no synthetic ordering"; NAV's response will tell us if the original
// is actually unknown.
func findParentSubmission(ctx context.Context, store SubmissionStore, invoiceNumber string) string {
	rows, err := store.FindByInvoiceNumber(ctx, invoiceNumber)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].EventID
}

// AnnulInvoice submits a NAV manageAnnulment for an invoice number this
// account previously reported via CREATE. ANNUL is the "the report
// itself was malformed; please erase it" operation — distinct from
// STORNO, which is the normal business-reversal flow and is what
// Stripe-driven voids route to automatically.
//
// AnnulInvoice is not called by the Stripe webhook handler. Call it
// from your own admin tooling when you discover, out-of-band, that a
// prior CREATE was wrong (wrong tax number, wrong date, etc.). NAV will
// queue the annulment as VERIFICATION_PENDING and require manual
// approval in the NAV portal before applying it.
//
// Returns the NAV transactionId on success.
func (h *BridgeHandler) AnnulInvoice(ctx context.Context, invoiceNumber, reason string) (string, error) {
	if invoiceNumber == "" {
		return "", errors.New("stripenav: invoiceNumber is required")
	}
	if reason == "" {
		reason = "annulled by admin action"
	}
	ann := buildAnnulmentRecord(invoiceNumber, reason, h.cfg.Clock())
	payload, err := xml.Marshal(ann)
	if err != nil {
		return "", err
	}
	payload = append([]byte(xml.Header), payload...)
	res, err := h.cfg.navClient.AnnulInvoice(ctx, []nav.AnnulmentOperation{{InvoiceAnnulment: payload}})
	if err != nil {
		return "", err
	}
	return res.TransactionID, nil
}

func (h *BridgeHandler) processCreditNote(ctx context.Context, event *stripe.Event, sub *Submission) error {
	cn, err := decodeCreditNote(event)
	if err != nil {
		return permanent(err)
	}
	if cn.Invoice == nil {
		return permanent(errors.New("stripenav: credit note has no invoice reference"))
	}
	sub.InvoiceNumber = cn.Number
	issuedAt := h.cfg.Clock()
	if cn.EffectiveAt > 0 {
		issuedAt = time.Unix(cn.EffectiveAt, 0).UTC()
	} else if cn.Created > 0 {
		issuedAt = time.Unix(cn.Created, 0).UTC()
	}
	rate, err := h.rateFor(ctx, string(cn.Currency), issuedAt)
	if err != nil {
		return err
	}
	// The credit note maps to a MODIFY against the original invoice.
	// We synthesise a minimal Stripe-Invoice-shaped object for the
	// mapper: the credit note's lines become the modification lines.
	// Note: the original invoice's line count is unknown here (the
	// event carries only the credit note), so the mapper's
	// OriginalLineCount fallback applies — wrong for originals whose
	// line count differs. Tracked in the credit-note roadmap item.
	invForMap := creditNoteAsInvoice(cn)
	mapped, err := invoicemap.MapInvoice(invForMap, invoicemap.MapOptions{
		Supplier:              h.cfg.Supplier,
		Operation:             invoicemap.OpModify,
		OriginalInvoiceNumber: cn.Invoice.Number,
		ModificationIndex:     1, // caller-supplied indexing is out of scope for v0
		ExchangeRateToHUF:     rate,
	})
	if err != nil {
		return permanent(err)
	}
	return h.persistMapped(ctx, sub, mapped, "MODIFY")
}

// rateFor resolves the foreign→HUF rate for the document issued at the
// given time. A missing provider is a configuration gap (permanent); a
// provider call failure is transient and worth a Stripe redelivery.
func (h *BridgeHandler) rateFor(ctx context.Context, currency string, at time.Time) (string, error) {
	if strings.EqualFold(currency, "HUF") {
		return "", nil
	}
	if h.cfg.ExchangeRateProvider == nil {
		return "", permanent(fmt.Errorf("stripenav: invoice currency %s requires ExchangeRateProvider", currency))
	}
	return h.cfg.ExchangeRateProvider(ctx, currency, at)
}

// decodeInvoice extracts a *stripe.Invoice from the event's Data.Raw blob.
func decodeInvoice(event *stripe.Event) (*stripe.Invoice, error) {
	if len(event.Data.Raw) == 0 {
		return nil, errors.New("stripenav: event has no data")
	}
	var inv stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
		return nil, fmt.Errorf("decode invoice: %w", err)
	}
	return &inv, nil
}

func decodeCreditNote(event *stripe.Event) (*stripe.CreditNote, error) {
	if len(event.Data.Raw) == 0 {
		return nil, errors.New("stripenav: event has no data")
	}
	var cn stripe.CreditNote
	if err := json.Unmarshal(event.Data.Raw, &cn); err != nil {
		return nil, fmt.Errorf("decode credit note: %w", err)
	}
	return &cn, nil
}
