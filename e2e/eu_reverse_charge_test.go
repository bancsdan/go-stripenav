//go:build navtest

package e2e

import (
	"net/http"
	"testing"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_EUReverseCharge_AcceptedAtNAV submits an invoice to a
// German B2B customer with their EU VAT number, billed at 0% VAT —
// the shape Hungarian VAT law produces under §37 reverse-charge
// (the buyer self-accounts in their own country). The mapper
// classifies the customer as OTHER + communityVatNumber.
//
// Caveat: the library currently ships a bare 0% VAT line rather than
// a `vatOutOfScope` block with a reason code. NAV's test environment
// accepts it, but a strict audit would expect the structured
// out-of-scope shape. Implementing that is roadmap item #3 — this
// test pins the current behaviour and will need updating when the
// vatOutOfScope/vatExemption mapping lands.
func TestE2E_EUReverseCharge_AcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	h, store, cleanup := newHarness(t, env)
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	eventID := "evt_" + invoiceNumber

	body := buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       eventID,
		InvoiceNumber: invoiceNumber,
		Lines: []invoiceLineOpts{
			{
				Description:   "Consulting (reverse-charge)",
				NetAmount:     1_000_000,
				VatRatePct:    0,
				VatRatePctSet: true,
			},
		},
		CustomerName: "Beispiel Käufer GmbH",
		CustomerAddress: map[string]string{
			"line1":       "Hauptstraße 17",
			"city":        "München",
			"postal_code": "80331",
			"country":     "DE",
		},
		CustomerTaxIDs: []map[string]string{
			{"type": "eu_vat", "value": "DE123456789"},
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
	t.Logf("E2E EU reverse-charge ok: invoice=%s txid=%s", invoiceNumber, got.TransactionID)
}
