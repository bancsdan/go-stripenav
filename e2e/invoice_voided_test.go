//go:build navtest

package e2e

import (
	"net/http"
	"testing"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_InvoiceVoided_StornoAcceptedAtNAV drives the full
// invoice → storno chain: finalize one invoice (CREATE), wait for it
// to be accepted at NAV, then send the matching invoice.voided event
// for the same invoice (STORNO). The worker is expected to recognise
// the parent CREATE is accepted and submit the STORNO against it; the
// STORNO carries its own transactionId distinct from the original.
//
// This exercises the parent-dependency tracking in the worker and the
// invoiceAsStorno sign-flip path.
func TestE2E_InvoiceVoided_StornoAcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	h, store, cleanup := newHarness(t, env)
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	finalizedEventID := "evt_finalized_" + invoiceNumber
	voidedEventID := "evt_voided_" + invoiceNumber

	// Step 1: finalize the invoice (CREATE).
	body := buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       finalizedEventID,
		InvoiceNumber: invoiceNumber,
		NetAmountFt:   1_000_000,
	})
	rec := postSignedEvent(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("finalize: handler returned %d; body=%s", rec.Code, rec.Body.String())
	}
	accepted := waitForStatus(t, store, finalizedEventID, stripenav.StatusAccepted, env.AcceptTimeout)
	if accepted.TransactionID == "" {
		t.Fatalf("finalize accepted but TransactionID empty: %+v", accepted)
	}
	t.Logf("E2E finalize ok: invoice=%s create_txid=%s", invoiceNumber, accepted.TransactionID)

	// Step 2: void the invoice (STORNO). The worker should observe
	// that the parent CREATE is accepted at NAV and submit the
	// STORNO against it.
	body = buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       voidedEventID,
		InvoiceNumber: invoiceNumber,
		NetAmountFt:   1_000_000,
		Type:          "invoice.voided",
	})
	rec = postSignedEvent(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("void: handler returned %d; body=%s", rec.Code, rec.Body.String())
	}
	storno := waitForStatus(t, store, voidedEventID, stripenav.StatusAccepted, env.AcceptTimeout)
	if storno.TransactionID == "" {
		t.Fatalf("storno accepted but TransactionID empty: %+v", storno)
	}
	if storno.TransactionID == accepted.TransactionID {
		t.Fatalf("storno reused create's TransactionID %q — expected a distinct id",
			storno.TransactionID)
	}
	t.Logf("E2E storno ok: invoice=%s create_txid=%s storno_txid=%s",
		invoiceNumber, accepted.TransactionID, storno.TransactionID)
}
