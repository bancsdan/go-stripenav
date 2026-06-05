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

	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav"
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

	// Store persists submission state. Required.
	Store SubmissionStore

	// ExchangeRateProvider returns the foreign→HUF exchange rate for
	// the given currency code at the given time. Required when invoices
	// in non-HUF currencies are expected; if nil, non-HUF invoices will
	// fail mapping.
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
	// *nav.Client from the NAV config. Tests in this package can poke at
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
		client, err := nav.NewClient(cfg.NAV)
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
			Supplier:      cfg.Supplier,
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
		ctx, cancel := context.WithCancel(context.Background())
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
	if cfg.Store == nil {
		return errors.New("stripenav: Config.Store is required")
	}
	if cfg.Supplier.TaxNumber == "" {
		return errors.New("stripenav: Config.Supplier.TaxNumber is required")
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
		// We never fail-close to Stripe: log + 200 so the worker can
		// retry. The error is recorded on the submission record.
		h.cfg.Logger.Error("stripenav: process event", "event_id", event.ID, "type", event.Type, "err", err)
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
		return err
	}
	rate, err := h.rateFor(ctx, string(inv.Currency))
	if err != nil {
		return err
	}

	var (
		invForMap *stripe.Invoice
		opts      mapping.MapOptions
		op        string
	)
	switch event.Type {
	case "invoice.finalized":
		invForMap = inv
		op = "CREATE"
		opts = mapping.MapOptions{
			Supplier:          h.cfg.Supplier,
			Operation:         mapping.OpCreate,
			ExchangeRateToHUF: rate,
		}
	case "invoice.voided", "invoice.marked_uncollectible":
		invForMap = invoiceAsStorno(inv, h.cfg.Clock())
		op = "STORNO"
		opts = mapping.MapOptions{
			Supplier:              h.cfg.Supplier,
			Operation:             mapping.OpStorno,
			OriginalInvoiceNumber: inv.Number,
			ExchangeRateToHUF:     rate,
		}
		// Record the dependency on the prior CREATE so the worker can
		// wait for NAV to finish processing it before submitting this
		// storno. NAV rejects stornos whose original is still in
		// PROCESSING / SAVED state.
		if parent := findParentSubmission(ctx, h.cfg.Store, inv.Number); parent != "" {
			sub.ParentEventID = parent
		}
	default:
		return fmt.Errorf("stripenav: unexpected invoice event %q", event.Type)
	}

	sub.InvoiceNumber = invForMap.Number
	sub.Operation = op

	mapped, err := mapping.MapInvoice(invForMap, opts)
	if err != nil {
		return err
	}
	payload, err := xml.Marshal(mapped)
	if err != nil {
		return err
	}
	payload = append([]byte(xml.Header), payload...)
	sub.RawEvent = payload // store the mapped XML so the worker can retry without re-mapping.

	if err := h.cfg.Store.Put(ctx, *sub); err != nil {
		return err
	}
	if h.worker != nil {
		h.worker.Wakeup()
	}
	return nil
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
		return err
	}
	if cn.Invoice == nil {
		return errors.New("stripenav: credit note has no invoice reference")
	}
	sub.InvoiceNumber = cn.Number
	rate, err := h.rateFor(ctx, string(cn.Currency))
	if err != nil {
		return err
	}
	// The credit note maps to a MODIFY against the original invoice.
	// We synthesise a minimal Stripe-Invoice-shaped object for the
	// mapper: the credit note's lines become the modification lines.
	invForMap := creditNoteAsInvoice(cn)
	mapped, err := mapping.MapInvoice(invForMap, mapping.MapOptions{
		Supplier:              h.cfg.Supplier,
		Operation:             mapping.OpModify,
		OriginalInvoiceNumber: cn.Invoice.Number,
		ModificationIndex:     1, // caller-supplied indexing is out of scope for v0
		ExchangeRateToHUF:     rate,
	})
	if err != nil {
		return err
	}
	payload, err := xml.Marshal(mapped)
	if err != nil {
		return err
	}
	payload = append([]byte(xml.Header), payload...)
	sub.RawEvent = payload
	sub.Operation = "MODIFY"
	if err := h.cfg.Store.Put(ctx, *sub); err != nil {
		return err
	}
	if h.worker != nil {
		h.worker.Wakeup()
	}
	return nil
}

func (h *BridgeHandler) rateFor(ctx context.Context, currency string) (string, error) {
	if strings.EqualFold(currency, "HUF") {
		return "", nil
	}
	if h.cfg.ExchangeRateProvider == nil {
		return "", fmt.Errorf("stripenav: invoice currency %s requires ExchangeRateProvider", currency)
	}
	return h.cfg.ExchangeRateProvider(ctx, currency, h.cfg.Clock())
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
