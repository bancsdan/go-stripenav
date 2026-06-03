//go:build navtest

package e2e

import (
	"net/http"
	"testing"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_MixedVatRates_AcceptedAtNAV submits an invoice with two
// lines at different Hungarian VAT rates (27% standard + 5% reduced).
// Exercises the per-rate summary bucketing — the mapper emits one
// summaryByVatRate block per distinct rate, and NAV validates that
// the per-rate sums reconcile against the invoice totals. A bucketing
// or rounding bug would surface as a NAV business-validation error.
func TestE2E_MixedVatRates_AcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	h, store, cleanup := newHarness(t, env)
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	eventID := "evt_" + invoiceNumber

	body := buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       eventID,
		InvoiceNumber: invoiceNumber,
		Lines: []invoiceLineOpts{
			// Standard rate (27%) line: services, software, most goods.
			{Description: "SaaS subscription", NetAmount: 800_000, VatRatePct: 27},
			// Reduced rate (5%) line: e.g. specific food categories,
			// medicine, books. Pinned here just to drive a second
			// vatRateSummary bucket.
			{Description: "Reduced-rate item", NetAmount: 200_000, VatRatePct: 5},
		},
	})
	rec := postSignedEvent(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got := waitForStatus(t, store, eventID, stripenav.StatusAccepted, env.AcceptTimeout)
	if got.TransactionID == "" {
		t.Fatalf("submission accepted but TransactionID empty: %+v", got)
	}
	t.Logf("E2E mixed-rates ok: invoice=%s txid=%s", invoiceNumber, got.TransactionID)
}
