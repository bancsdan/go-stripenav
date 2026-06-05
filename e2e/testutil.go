//go:build navtest

// Package e2e holds end-to-end tests that exercise the full bridge —
// HTTP handler → in-memory store → background worker → real NAV
// Online Számla v3.0 test environment — without any fakes.
//
// The package is gated behind the `navtest` build tag so normal
// `go test ./...` never touches NAV. To run:
//
//	go test -tags=navtest -count=1 ./e2e/...
//
// Required env vars (all empty → t.Skip):
//
//	NAV_LOGIN, NAV_PASSWORD, NAV_TAX_NUMBER, NAV_SIGN_KEY, NAV_EXCHANGE_KEY
//	SUPPLIER_TAX_NUMBER, SUPPLIER_NAME, SUPPLIER_POSTAL_CODE, SUPPLIER_CITY
//
// Optional:
//
//	NAV_BASE_URL          (default: nav.TestBaseURL)
//	SUPPLIER_COUNTRY      (default: HU)
//	E2E_ACCEPT_TIMEOUT    (default: 150s — max wait for a submission to reach accepted;
//	                       NAV's test env can sit in PROCESSING for a minute-plus during
//	                       load spikes, so the default is intentionally generous)
package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/storeinmem"
	"github.com/stripe/stripe-go/v82/webhook"
)

// stripeSecret is the webhook secret used for signing payloads in the
// harness. We control both signer and verifier, so the value is
// arbitrary — just non-empty.
const stripeSecret = "whsec_e2e_harness"

// e2eEnv is the loaded test-environment configuration. Missing required
// vars cause the calling test to skip cleanly.
type e2eEnv struct {
	NAV      nav.Config
	Supplier mapping.Supplier
	// AcceptTimeout caps how long waitForStatus polls before failing.
	AcceptTimeout time.Duration
}

// loadEnv reads NAV creds + supplier identity from the environment.
// Calls t.Skip if any required var is missing, so the harness no-ops
// in environments without NAV credentials.
func loadEnv(t *testing.T) e2eEnv {
	t.Helper()
	required := []string{
		"NAV_LOGIN", "NAV_PASSWORD", "NAV_TAX_NUMBER",
		"NAV_SIGN_KEY", "NAV_EXCHANGE_KEY",
		"SUPPLIER_TAX_NUMBER", "SUPPLIER_NAME",
		"SUPPLIER_POSTAL_CODE", "SUPPLIER_CITY",
	}
	for _, k := range required {
		if os.Getenv(k) == "" {
			t.Skipf("skipping E2E: %s not set", k)
		}
	}

	baseURL := os.Getenv("NAV_BASE_URL")
	if baseURL == "" {
		baseURL = nav.TestBaseURL
	}
	country := os.Getenv("SUPPLIER_COUNTRY")
	if country == "" {
		country = "HU"
	}
	timeout := 150 * time.Second
	if v := os.Getenv("E2E_ACCEPT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			timeout = d
		}
	}

	return e2eEnv{
		NAV: nav.Config{
			BaseURL:     baseURL,
			Login:       os.Getenv("NAV_LOGIN"),
			Password:    os.Getenv("NAV_PASSWORD"),
			TaxNumber:   os.Getenv("NAV_TAX_NUMBER"),
			SignKey:     os.Getenv("NAV_SIGN_KEY"),
			ExchangeKey: os.Getenv("NAV_EXCHANGE_KEY"),
			Software: nav.Software{
				ID:             "HU00000000GOSTRPNV",
				Name:           "gostripenav-e2e",
				Operation:      "LOCAL_SOFTWARE",
				MainVersion:    "0.0.1",
				DevName:        "gostripenav-e2e",
				DevContact:     "e2e@example.com",
				DevCountryCode: "HU",
			},
		},
		Supplier: mapping.Supplier{
			TaxNumber: os.Getenv("SUPPLIER_TAX_NUMBER"),
			Name:      os.Getenv("SUPPLIER_NAME"),
			Address: mapping.Address{
				CountryCode:      country,
				PostalCode:       os.Getenv("SUPPLIER_POSTAL_CODE"),
				City:             os.Getenv("SUPPLIER_CITY"),
				AdditionalDetail: os.Getenv("SUPPLIER_ADDRESS"),
			},
		},
		AcceptTimeout: timeout,
	}
}

// harnessOpts overrides defaults on the bridge under test. Currently the
// only knob is the exchange-rate provider, needed for non-HUF invoices.
type harnessOpts struct {
	ExchangeRate func(ctx context.Context, currency string, at time.Time) (string, error)
}

// newHarness wires a real bridge: in-memory store, real nav.Client
// against the test endpoint, worker enabled. The returned cleanup
// shuts the handler down. Pass a harnessOpts with ExchangeRate set
// for non-HUF invoices.
func newHarness(t *testing.T, env e2eEnv, opts ...harnessOpts) (*stripenav.BridgeHandler, *storeinmem.Store, func()) {
	t.Helper()
	store := storeinmem.New()
	cfg := stripenav.Config{
		StripeWebhookSecret: stripeSecret,
		NAV:                 env.NAV,
		Supplier:            env.Supplier,
		Store:               store,
		// Tighten pacing so tests don't sit waiting on the default 10s sleep.
		WorkerMaxSleep:     500 * time.Millisecond,
		WorkerPollInterval: 2 * time.Second,
	}
	for _, o := range opts {
		if o.ExchangeRate != nil {
			cfg.ExchangeRateProvider = o.ExchangeRate
		}
	}
	h, err := stripenav.Handler(cfg)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	}
	return h, store, cleanup
}

// postSignedEvent serialises body as a Stripe-shaped payload, signs it
// with stripeSecret, POSTs to the handler, and returns the response
// recorder so the caller can assert status.
func postSignedEvent(t *testing.T, h http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	ts := time.Now()
	sig := webhook.ComputeSignature(ts, body, stripeSecret)
	header := fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(sig))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", header)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// invoiceEventOpts configures buildInvoiceEvent. Zero-valued fields fall
// back to the simplest realistic shape (HUF, exclusive 27% VAT, no
// customer info, finalized event).
type invoiceEventOpts struct {
	EventID       string
	InvoiceNumber string
	// NetAmountFt is the line's net amount in Stripe minor units (×100
	// for HUF, USD, EUR; ×1 for zero-decimal currencies like JPY). 27%
	// VAT is computed on top by default. Ignored when Lines is set.
	NetAmountFt int64

	// Currency is the lowercased ISO code (huf, usd, eur, …). Default
	// "huf". For non-HUF, the harness must be created with an
	// ExchangeRate provider via harnessOpts.
	Currency string

	// Lines, when non-empty, overrides the single-line shortcut. Each
	// entry produces one line in the Stripe payload with the given
	// net amount and VAT rate. Useful for multi-rate and mixed-line
	// scenarios.
	Lines []invoiceLineOpts

	// Type defaults to "invoice.finalized". Also supports
	// "invoice.voided" — flips status to "void" and populates
	// voided_at, which the bridge routes to a STORNO submission.
	Type string

	// InclusiveTax sets the single shortcut line's tax_behavior to
	// "inclusive" (line `amount` becomes gross). Ignored when Lines
	// is set; per-line Inclusive on invoiceLineOpts wins there.
	InclusiveTax bool

	// Subscription sets billing_reason=subscription_cycle and the
	// period_start/period_end fields, triggering the §58 periodic
	// settlement branch in the mapper.
	Subscription bool
	PeriodStart  time.Time
	PeriodEnd    time.Time

	// Customer fields. Each one is omitted from the JSON when zero.
	// CustomerTaxIDs entries look like {"type": "hu_tin", "value":
	// "12345678-1-23"}.
	CustomerName    string
	CustomerEmail   string
	CustomerAddress map[string]string
	CustomerTaxIDs  []map[string]string
}

// invoiceLineOpts shapes a single Stripe invoice line for multi-line
// scenarios.
type invoiceLineOpts struct {
	Description string
	// NetAmount is the net in Stripe minor units (×100 for most
	// currencies, ×1 for zero-decimal). When Inclusive=true the
	// line's emitted `amount` field is NetAmount+VAT (gross).
	NetAmount int64
	// VatRatePct expressed as integer percent (27 → 27%, 5 → 5%,
	// 0 → no VAT). 27 is the default if zero AND VatRateSet=false.
	VatRatePct    int
	VatRatePctSet bool
	Quantity      int64
	Inclusive     bool
}

// buildInvoiceEvent returns a Stripe-shaped event body for the
// configured invoice. invoiceNumber must be unique per NAV supplier —
// NAV rejects duplicates with INVOICE_NUMBER_NOT_UNIQUE.
func buildInvoiceEvent(t *testing.T, opts invoiceEventOpts) []byte {
	t.Helper()
	if opts.Type == "" {
		opts.Type = "invoice.finalized"
	}
	if opts.Currency == "" {
		opts.Currency = "huf"
	}

	lineData := []map[string]any{}
	if len(opts.Lines) == 0 {
		// Single-line shortcut from NetAmountFt at 27% VAT.
		vat := opts.NetAmountFt * 27 / 100
		amount := opts.NetAmountFt
		taxEntry := map[string]any{"amount": vat}
		if opts.InclusiveTax {
			amount = opts.NetAmountFt + vat
			taxEntry["tax_behavior"] = "inclusive"
		}
		lineData = append(lineData, map[string]any{
			"description": "E2E harness line",
			"amount":      amount,
			"quantity":    1,
			"taxes":       []map[string]any{taxEntry},
		})
	} else {
		for i, l := range opts.Lines {
			pct := l.VatRatePct
			if !l.VatRatePctSet && pct == 0 {
				pct = 27 // sensible default; explicit 0 must set VatRatePctSet=true
			}
			vat := l.NetAmount * int64(pct) / 100
			amount := l.NetAmount
			taxEntry := map[string]any{"amount": vat}
			if l.Inclusive {
				amount = l.NetAmount + vat
				taxEntry["tax_behavior"] = "inclusive"
			}
			desc := l.Description
			if desc == "" {
				desc = fmt.Sprintf("Line %d", i+1)
			}
			qty := l.Quantity
			if qty == 0 {
				qty = 1
			}
			lineData = append(lineData, map[string]any{
				"description": desc,
				"amount":      amount,
				"quantity":    qty,
				"taxes":       []map[string]any{taxEntry},
			})
		}
	}

	st := map[string]any{
		"finalized_at": time.Now().Add(-time.Minute).Unix(),
	}
	status := "open"
	if opts.Type == "invoice.voided" {
		status = "void"
		st["voided_at"] = time.Now().Unix()
	}

	inv := map[string]any{
		"id":                 "in_" + opts.InvoiceNumber,
		"object":             "invoice",
		"number":             opts.InvoiceNumber,
		"currency":           opts.Currency,
		"status":             status,
		"status_transitions": st,
		"lines":              map[string]any{"data": lineData},
	}

	if opts.Subscription {
		inv["billing_reason"] = "subscription_cycle"
		inv["period_start"] = opts.PeriodStart.Unix()
		inv["period_end"] = opts.PeriodEnd.Unix()
	}
	if opts.CustomerName != "" {
		inv["customer_name"] = opts.CustomerName
	}
	if opts.CustomerEmail != "" {
		inv["customer_email"] = opts.CustomerEmail
	}
	if len(opts.CustomerAddress) > 0 {
		addr := make(map[string]any, len(opts.CustomerAddress))
		for k, v := range opts.CustomerAddress {
			addr[k] = v
		}
		inv["customer_address"] = addr
	}
	if len(opts.CustomerTaxIDs) > 0 {
		tids := make([]map[string]any, 0, len(opts.CustomerTaxIDs))
		for _, ti := range opts.CustomerTaxIDs {
			entry := make(map[string]any, len(ti))
			for k, v := range ti {
				entry[k] = v
			}
			tids = append(tids, entry)
		}
		inv["customer_tax_ids"] = tids
	}

	event := map[string]any{
		"id":      opts.EventID,
		"object":  "event",
		"type":    opts.Type,
		"created": time.Now().Unix(),
		"data":    map[string]any{"object": inv},
	}
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return b
}

// waitForStatus polls the store every 500ms until the submission with
// eventID reaches want, or the timeout elapses. On timeout it logs the
// last observed submission state so the failure is debuggable.
func waitForStatus(t *testing.T, store stripenav.SubmissionStore, eventID string, want stripenav.SubmissionStatus, timeout time.Duration) stripenav.Submission {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last stripenav.Submission
	for time.Now().Before(deadline) {
		got, err := store.Get(context.Background(), eventID)
		if err == nil {
			last = got
			if got.Status == want {
				return got
			}
			// Terminal-but-wrong: fail fast, no point polling further.
			if got.Status == stripenav.StatusRejected || got.Status == stripenav.StatusAborted {
				t.Fatalf("submission terminal at %s (want %s): attempts=%d txid=%s err=%q",
					got.Status, want, got.Attempts, got.TransactionID, got.LastError)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for status=%s after %s; last seen: status=%s attempts=%d txid=%s err=%q",
		want, timeout, last.Status, last.Attempts, last.TransactionID, last.LastError)
	return last
}

// uniqueInvoiceNumber returns a per-run-unique invoice number that
// fits NAV's allowed character set. Format: E2E-YYYYMMDDHHMMSSnnn.
func uniqueInvoiceNumber() string {
	now := time.Now().UTC()
	return fmt.Sprintf("E2E-%s%03d", now.Format("20060102150405"), now.Nanosecond()/1_000_000)
}

