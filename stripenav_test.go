package stripenav_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
	"github.com/bancsdan/go-stripenav/storeinmem"
	"github.com/stripe/stripe-go/v82/webhook"
)

func signStripeWebhook(payload []byte, secret string, ts time.Time) string {
	sig := webhook.ComputeSignature(ts, payload, secret)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(sig))
}

func newFinalizedInvoiceEvent(t *testing.T, number string) []byte {
	t.Helper()
	inv := map[string]any{
		"id":       "in_" + number,
		"object":   "invoice",
		"number":   number,
		"currency": "huf",
		"status":   "open",
		"status_transitions": map[string]any{
			"finalized_at": time.Now().Add(-time.Minute).Unix(),
		},
		"lines": map[string]any{
			"data": []map[string]any{
				{
					"description": "Service line",
					"amount":      1_000_000,
					"quantity":    1,
					"taxes":       []map[string]any{{"amount": 270_000}},
				},
			},
		},
	}
	return marshalEvent(t, "evt_"+number, "invoice.finalized", inv)
}

func newVoidedInvoiceEvent(t *testing.T, number string) []byte {
	t.Helper()
	inv := map[string]any{
		"id":       "in_" + number,
		"object":   "invoice",
		"number":   number,
		"currency": "huf",
		"status":   "void",
		"status_transitions": map[string]any{
			"finalized_at": time.Now().Add(-time.Hour).Unix(),
		},
		"lines": map[string]any{
			"data": []map[string]any{
				{
					"description": "Service line",
					"amount":      1_000_000,
					"quantity":    1,
					"taxes":       []map[string]any{{"amount": 270_000}},
				},
			},
		},
	}
	return marshalEvent(t, "evt_void_"+number, "invoice.voided", inv)
}

func marshalEvent(t *testing.T, id, typ string, inv map[string]any) []byte {
	t.Helper()
	event := map[string]any{
		"id":      id,
		"object":  "event",
		"type":    typ,
		"created": time.Now().Unix(),
		"data":    map[string]any{"object": inv},
	}
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	return b
}

// tickOnce builds a fresh worker bound to the same store + fake NAV
// client and drives it through a single tick, so test cases can verify
// end-to-end submission behaviour after the handler persists.
func tickOnce(t *testing.T, store stripenav.SubmissionStore, fake stripenav.NAVClient) {
	t.Helper()
	w, err := stripenav.NewWorker(stripenav.WorkerConfig{
		Store:        store,
		Client:       fake,
		TickInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatalf("worker tick: %v", err)
	}
}

// handlerWithFake assembles a Config and Handler around a fake NAV client.
// The fake is injected via the public WithNAVClient option so this
// external test package never has to touch unexported fields.
func handlerWithFake(t *testing.T, fake stripenav.NAVClient) (*stripenav.BridgeHandler, stripenav.Config) {
	t.Helper()
	cfg := stripenav.Config{
		StripeWebhookSecret: "whsec_test",
		NAV: nav.Config{
			BaseURL:     "https://example/v3",
			Login:       "user",
			Password:    "p",
			TaxNumber:   "11111111",
			SignKey:     "k",
			ExchangeKey: "0123456789ABCDEF",
			Software:    nav.Software{ID: "SW", Name: "n", Operation: "LOCAL_SOFTWARE"},
		},
		Supplier: mapping.Supplier{
			TaxNumber: "12345678-9-01",
			Name:      "Test Merchant Kft.",
			Address:   mapping.Address{CountryCode: "HU", PostalCode: "1011", City: "Budapest"},
		},
		Store:         storeinmem.New(),
		DisableWorker: true,
	}
	h, err := stripenav.Handler(cfg, stripenav.WithNAVClient(fake))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h, cfg
}

func TestHandler_RejectsMissingSignature(t *testing.T) {
	h, _ := handlerWithFake(t, &fakeNAVClient{})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandler_RejectsBadSignature(t *testing.T) {
	h, _ := handlerWithFake(t, &fakeNAVClient{})
	payload := []byte(`{"id":"evt_x","type":"invoice.finalized"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandler_IgnoresUnrelatedEventType(t *testing.T) {
	h, cfg := handlerWithFake(t, &fakeNAVClient{
		submitFn: func(context.Context, []nav.InvoiceOperation) (nav.SubmitResult, error) {
			t.Fatal("submit should not be called for unrelated events")
			return nav.SubmitResult{}, nil
		},
	})
	payload := []byte(`{"id":"evt_y","type":"customer.created","created":1,"data":{"object":{}}}`)
	sig := signStripeWebhook(payload, cfg.StripeWebhookSecret, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHandler_VoidedInvoiceSubmitsStorno(t *testing.T) {
	var got nav.InvoiceOperation
	fake := &fakeNAVClient{
		submitFn: func(_ context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			if len(ops) != 1 {
				return nav.SubmitResult{}, fmt.Errorf("unexpected ops: %+v", ops)
			}
			got = ops[0]
			return nav.SubmitResult{TransactionID: "T-STORNO"}, nil
		},
		annulFn: func(context.Context, []nav.AnnulmentOperation) (nav.SubmitResult, error) {
			t.Fatalf("AnnulInvoice must not be called for invoice.voided (we use STORNO now)")
			return nav.SubmitResult{}, nil
		},
	}
	h, cfg := handlerWithFake(t, fake)

	payload := newVoidedInvoiceEvent(t, "2026-V1")
	sig := signStripeWebhook(payload, cfg.StripeWebhookSecret, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body)
	}
	tickOnce(t, cfg.Store, fake)
	if got.Operation != "STORNO" {
		t.Fatalf("operation = %q, want STORNO", got.Operation)
	}
	for _, want := range [][]byte{
		[]byte("<invoiceNumber>2026-V1-STORNO</invoiceNumber>"),
		[]byte("<originalInvoiceNumber>2026-V1</originalInvoiceNumber>"),
		[]byte("<lineNumberReference>2</lineNumberReference>"),
		[]byte("<lineOperation>CREATE</lineOperation>"),
		[]byte("<lineNetAmount>-10000.00</lineNetAmount>"),
	} {
		if !bytes.Contains(got.InvoiceData, want) {
			t.Errorf("payload missing %q\nbody=%s", want, got.InvoiceData)
		}
	}

	sub, err := cfg.Store.Get(context.Background(), "evt_void_2026-V1")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if sub.InvoiceNumber != "2026-V1-STORNO" || sub.Status != stripenav.StatusSubmitted || sub.TransactionID != "T-STORNO" {
		t.Fatalf("submission state: %+v", sub)
	}
}

func TestHandler_FinalizedInvoiceSubmitsAndStores(t *testing.T) {
	fake := &fakeNAVClient{
		submitFn: func(_ context.Context, ops []nav.InvoiceOperation) (nav.SubmitResult, error) {
			if len(ops) != 1 || ops[0].Operation != "CREATE" {
				return nav.SubmitResult{}, fmt.Errorf("unexpected ops: %+v", ops)
			}
			if !bytes.Contains(ops[0].InvoiceData, []byte("<invoiceNumber>2026-1</invoiceNumber>")) {
				return nav.SubmitResult{}, errors.New("payload missing invoice number")
			}
			return nav.SubmitResult{TransactionID: "T-OK"}, nil
		},
	}
	h, cfg := handlerWithFake(t, fake)

	payload := newFinalizedInvoiceEvent(t, "2026-1")
	sig := signStripeWebhook(payload, cfg.StripeWebhookSecret, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body)
	}
	tickOnce(t, cfg.Store, fake)
	got, err := cfg.Store.Get(context.Background(), "evt_2026-1")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if got.Status != stripenav.StatusSubmitted || got.TransactionID != "T-OK" {
		t.Fatalf("submission state: %+v", got)
	}
}

func TestHandler_Dedup(t *testing.T) {
	calls := 0
	fake := &fakeNAVClient{
		submitFn: func(context.Context, []nav.InvoiceOperation) (nav.SubmitResult, error) {
			calls++
			return nav.SubmitResult{TransactionID: "T"}, nil
		},
	}
	h, cfg := handlerWithFake(t, fake)
	payload := newFinalizedInvoiceEvent(t, "2026-2")
	sig := signStripeWebhook(payload, cfg.StripeWebhookSecret, time.Now())

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
		req.Header.Set("Stripe-Signature", sig)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("iter %d status = %d", i, w.Code)
		}
	}
	tickOnce(t, cfg.Store, fake)
	if calls != 1 {
		t.Fatalf("NAV submit called %d times for the same event, want 1", calls)
	}
}

func TestHandler_NavErrorStoredButResponds200(t *testing.T) {
	fake := &fakeNAVClient{
		submitFn: func(context.Context, []nav.InvoiceOperation) (nav.SubmitResult, error) {
			return nav.SubmitResult{}, &nav.NAVError{HTTPStatus: 500, Code: "INTERNAL_ERROR", Retriable: true, Message: "boom"}
		},
	}
	h, cfg := handlerWithFake(t, fake)
	payload := newFinalizedInvoiceEvent(t, "2026-3")
	sig := signStripeWebhook(payload, cfg.StripeWebhookSecret, time.Now())
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	tickOnce(t, cfg.Store, fake)
	got, _ := cfg.Store.Get(context.Background(), "evt_2026-3")
	if got.Status != stripenav.StatusPending {
		t.Fatalf("expected pending after retriable failure, got %s", got.Status)
	}
	if !strings.Contains(got.LastError, "boom") {
		t.Fatalf("expected error recorded, got %q", got.LastError)
	}
}

func TestHandler_InvalidConfig(t *testing.T) {
	_, err := stripenav.Handler(stripenav.Config{})
	if err == nil || !strings.Contains(err.Error(), "StripeWebhookSecret") {
		t.Fatalf("expected StripeWebhookSecret error, got %v", err)
	}
	_, err = stripenav.Handler(stripenav.Config{StripeWebhookSecret: "x"})
	if err == nil || !strings.Contains(err.Error(), "Store") {
		t.Fatalf("expected Store error, got %v", err)
	}
	_, err = stripenav.Handler(stripenav.Config{StripeWebhookSecret: "x", Store: storeinmem.New()})
	if err == nil || !strings.Contains(err.Error(), "Supplier") {
		t.Fatalf("expected Supplier error, got %v", err)
	}
}

func TestHandler_Shutdown(t *testing.T) {
	cfg := stripenav.Config{
		StripeWebhookSecret: "whsec_test",
		NAV: nav.Config{
			BaseURL: "https://example/v3", Login: "u", Password: "p", TaxNumber: "11111111",
			SignKey: "k", ExchangeKey: "0123456789ABCDEF",
			Software: nav.Software{ID: "SW", Name: "n", Operation: "LOCAL_SOFTWARE"},
		},
		Supplier: mapping.Supplier{TaxNumber: "12345678-9-01", Name: "T", Address: mapping.Address{CountryCode: "HU"}},
		Store:    storeinmem.New(),
	}
	fake := &fakeNAVClient{
		submitFn: func(context.Context, []nav.InvoiceOperation) (nav.SubmitResult, error) {
			return nav.SubmitResult{}, nil
		},
		statusFn: func(context.Context, string, bool) (schemas.QueryTransactionStatusResponse, error) {
			return schemas.QueryTransactionStatusResponse{}, nil
		},
	}
	h, err := stripenav.Handler(cfg, stripenav.WithNAVClient(fake))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
