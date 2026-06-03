//go:build navtest

package e2e

import (
	"net/http"
	"testing"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_DomesticCustomer_AcceptedAtNAV adds a Hungarian buyer with
// an hu_tin to the invoice. The mapper classifies them as DOMESTIC,
// splits the 11-digit composite into taxpayerId/vatCode/countyCode,
// and emits a structured customerInfo block. NAV cross-references the
// taxpayer; an incorrectly-shaped customerTaxNumber would be rejected.
// Customer fields used here are entirely synthetic — `23456789-2-09`
// is a made-up composite, not a real entity.
func TestE2E_DomesticCustomer_AcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	h, store, cleanup := newHarness(t, env)
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	eventID := "evt_" + invoiceNumber

	body := buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       eventID,
		InvoiceNumber: invoiceNumber,
		NetAmountFt:   1_000_000,
		CustomerName:  "Példa Vevő Kft.",
		CustomerEmail: "szamla@peldavevo.hu",
		CustomerAddress: map[string]string{
			"line1":       "Petőfi Sándor utca 42.",
			"city":        "Debrecen",
			"postal_code": "4025",
			"country":     "HU",
		},
		CustomerTaxIDs: []map[string]string{
			{"type": "hu_tin", "value": "23456789-2-09"},
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
	t.Logf("E2E domestic-customer ok: invoice=%s txid=%s", invoiceNumber, got.TransactionID)
}
