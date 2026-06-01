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
//	E2E_ACCEPT_TIMEOUT    (default: 90s — max wait for a submission to reach accepted)
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
	timeout := 90 * time.Second
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

// newHarness wires a real bridge: in-memory store, real nav.Client
// against the test endpoint, worker enabled. The returned cleanup
// shuts the handler down.
func newHarness(t *testing.T, env e2eEnv) (*stripenav.BridgeHandler, *storeinmem.Store, func()) {
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

// buildInvoiceFinalizedEvent returns a Stripe-shaped invoice.finalized
// event body. invoiceNumber must be unique per NAV supplier — NAV
// rejects duplicates with INVOICE_NUMBER_NOT_UNIQUE. netAmountFt is the
// line subtotal before VAT (matches Stripe's invoice.lines[].amount
// semantics); 27% Hungarian standard VAT is added on top.
func buildInvoiceFinalizedEvent(t *testing.T, eventID, invoiceNumber string, netAmountFt int64) []byte {
	t.Helper()
	tax := netAmountFt * 27 / 100 // 27% VAT on net
	inv := map[string]any{
		"id":       "in_" + invoiceNumber,
		"object":   "invoice",
		"number":   invoiceNumber,
		"currency": "huf",
		"status":   "open",
		"status_transitions": map[string]any{
			"finalized_at": time.Now().Add(-time.Minute).Unix(),
		},
		"lines": map[string]any{
			"data": []map[string]any{
				{
					"description": "E2E harness line",
					"amount":      netAmountFt,
					"quantity":    1,
					"taxes":       []map[string]any{{"amount": tax}},
				},
			},
		},
	}
	event := map[string]any{
		"id":      eventID,
		"object":  "event",
		"type":    "invoice.finalized",
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

