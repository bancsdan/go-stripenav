//go:build navtest

package e2e

import (
	"net/http"
	"testing"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_InclusiveTax_AcceptedAtNAV pins the inclusive-tax mapping
// path against the real NAV test environment. The line's `amount`
// field carries the gross (net + VAT) and the tax entry has
// tax_behavior=inclusive — the shape Stripe Tax produces for B2C
// pricing. Before the inclusive-tax fix the mapper would have computed
// a wrong VAT rate (~21% instead of 27%) and wrong totals; NAV would
// either reject for VAT-rate mismatch or silently accept incorrect
// figures. After the fix this should accept cleanly with the correct
// 27% rate.
func TestE2E_InclusiveTax_AcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	h, store, cleanup := newHarness(t, env)
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	eventID := "evt_" + invoiceNumber

	body := buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       eventID,
		InvoiceNumber: invoiceNumber,
		NetAmountFt:   1_000_000,
		InclusiveTax:  true,
	})
	rec := postSignedEvent(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got := waitForStatus(t, store, eventID, stripenav.StatusAccepted, env.AcceptTimeout)
	if got.TransactionID == "" {
		t.Fatalf("submission accepted but TransactionID empty: %+v", got)
	}
	t.Logf("E2E inclusive-tax ok: invoice=%s txid=%s", invoiceNumber, got.TransactionID)
}
