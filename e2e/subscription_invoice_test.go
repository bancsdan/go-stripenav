//go:build navtest

package e2e

import (
	"net/http"
	"testing"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
)

// TestE2E_SubscriptionInvoice_AcceptedAtNAV drives a §58 periodic-
// settlement invoice through the bridge. The event carries
// billing_reason=subscription_cycle plus a calendar-month period, so
// the mapper emits periodicalSettlement=true and the
// invoiceDeliveryPeriodStart/End pair. The assertion that NAV accepts
// the submission pins the contract on the §58 fields we added in the
// schema + mapper.
func TestE2E_SubscriptionInvoice_AcceptedAtNAV(t *testing.T) {
	env := loadEnv(t)
	h, store, cleanup := newHarness(t, env)
	defer cleanup()

	invoiceNumber := uniqueInvoiceNumber()
	eventID := "evt_" + invoiceNumber

	// Previous calendar month as the billing period.
	periodEnd := time.Now().UTC().Truncate(24 * time.Hour)
	periodStart := periodEnd.AddDate(0, -1, 0)

	body := buildInvoiceEvent(t, invoiceEventOpts{
		EventID:       eventID,
		InvoiceNumber: invoiceNumber,
		NetAmountFt:   1_000_000,
		Subscription:  true,
		PeriodStart:   periodStart,
		PeriodEnd:     periodEnd,
	})
	rec := postSignedEvent(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got := waitForStatus(t, store, eventID, stripenav.StatusAccepted, env.AcceptTimeout)
	if got.TransactionID == "" {
		t.Fatalf("submission accepted but TransactionID empty: %+v", got)
	}
	t.Logf("E2E subscription ok: invoice=%s period=%s/%s txid=%s",
		invoiceNumber,
		periodStart.Format("2006-01-02"), periodEnd.Format("2006-01-02"),
		got.TransactionID)
}
