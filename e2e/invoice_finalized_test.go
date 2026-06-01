//go:build navtest

package e2e

import (
	"net/http"
	"testing"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_InvoiceFinalized_AcceptedAtNAV drives a single invoice
// through the full bridge against the real NAV test environment:
//
//	signed invoice.finalized → handler → store → worker → NAV submit →
//	worker poll loop → store row reaches StatusAccepted
//
// On success the submission carries a real NAV transactionId. The test
// fails (rather than passing on skip) if the handler returns non-2xx
// or the submission lands in a terminal-but-wrong state.
func TestE2E_InvoiceFinalized_AcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	h, store, cleanup := newHarness(t, env)
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	eventID := "evt_" + invoiceNumber

	body := buildInvoiceFinalizedEvent(t, eventID, invoiceNumber, 1_000_000)
	rec := postSignedEvent(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got := waitForStatus(t, store, eventID, stripenav.StatusAccepted, env.AcceptTimeout)

	if got.TransactionID == "" {
		t.Fatalf("submission accepted but TransactionID empty: %+v", got)
	}
	t.Logf("E2E ok: invoice=%s eventID=%s txid=%s attempts=%d",
		invoiceNumber, eventID, got.TransactionID, got.Attempts)
}
