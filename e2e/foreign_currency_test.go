//go:build navtest

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_ForeignCurrency_AcceptedAtNAV submits a USD invoice with a
// supplier-supplied exchange rate via the harness ExchangeRateProvider.
// The mapper emits native-currency amounts on the line + an HUF summary
// computed at the rate. NAV checks both: the invoiceCurrency must be a
// valid ISO code and the HUF-converted summary must reconcile
// internally. Demonstrates the FC → HUF code path against real NAV.
func TestE2E_ForeignCurrency_AcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	// Stub provider — a real consumer would query MNB's daily rate.
	// 362 HUF / USD is a plausible test value; NAV doesn't validate
	// the rate against an external source.
	rate := func(ctx context.Context, currency string, at time.Time) (string, error) {
		return "362", nil
	}
	h, store, cleanup := newHarness(t, env, harnessOpts{ExchangeRate: rate})
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	eventID := "evt_" + invoiceNumber

	body := buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       eventID,
		InvoiceNumber: invoiceNumber,
		Currency:      "usd",
		// 100.00 USD net, 27.00 USD VAT (Stripe minor units ×100).
		NetAmountFt: 10_000,
	})
	rec := postSignedEvent(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got := waitForStatus(t, store, eventID, stripenav.StatusAccepted, env.AcceptTimeout)
	if got.TransactionID == "" {
		t.Fatalf("submission accepted but TransactionID empty: %+v", got)
	}
	t.Logf("E2E foreign-currency ok: invoice=%s txid=%s", invoiceNumber, got.TransactionID)
}
