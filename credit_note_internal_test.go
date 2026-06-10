package stripenav

import (
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82"
)

// TestInvoiceAsStorno_ResetsIssueDate pins that the cloned storno
// carries its OWN issue date (the `now` passed in), not the original
// invoice's finalize date. Áfa tv. §169 treats the storno as a
// separate document with its own issuance moment, and reusing the
// original's date misrepresents that.
func TestInvoiceAsStorno_ResetsIssueDate(t *testing.T) {
	originalFinalized := time.Date(2026, time.January, 15, 10, 0, 0, 0, time.UTC).Unix()
	stornoIssued := time.Date(2026, time.June, 3, 14, 30, 0, 0, time.UTC)

	original := &stripe.Invoice{
		Number: "2026/00001",
		StatusTransitions: &stripe.InvoiceStatusTransitions{
			FinalizedAt: originalFinalized,
		},
		Lines: &stripe.InvoiceLineItemList{
			Data: []*stripe.InvoiceLineItem{
				{Description: "Service", Amount: 1_000_000, Quantity: 1,
					Taxes: []*stripe.InvoiceLineItemTax{{Amount: 270_000}}},
			},
		},
	}

	clone := invoiceAsStorno(original, stornoIssued)

	if clone.Number != "2026/00001-STORNO" {
		t.Errorf("clone.Number = %q, want 2026/00001-STORNO", clone.Number)
	}
	if clone.StatusTransitions == nil {
		t.Fatalf("clone.StatusTransitions is nil")
	}
	if clone.StatusTransitions.FinalizedAt != stornoIssued.Unix() {
		t.Errorf("clone FinalizedAt = %d, want %d (= the new issuance time)",
			clone.StatusTransitions.FinalizedAt, stornoIssued.Unix())
	}
	// Original's StatusTransitions must not be mutated — the clone has
	// its own pointer.
	if original.StatusTransitions.FinalizedAt != originalFinalized {
		t.Errorf("original mutated: FinalizedAt = %d, want %d",
			original.StatusTransitions.FinalizedAt, originalFinalized)
	}
	// Lines must be sign-flipped on the clone but untouched on the
	// original.
	if clone.Lines.Data[0].Amount != -1_000_000 {
		t.Errorf("clone line amount = %d, want -1000000", clone.Lines.Data[0].Amount)
	}
	if original.Lines.Data[0].Amount != 1_000_000 {
		t.Errorf("original line amount mutated: %d", original.Lines.Data[0].Amount)
	}
}

// TestInvoiceAsStorno_PreservesTaxBehavior pins that the sign-flip
// carries tax_behavior through. Without it an inclusive-tax (B2C
// default) storno loses the gross/net distinction and the mapper
// misstates the reversal's net by the VAT amount.
func TestInvoiceAsStorno_PreservesTaxBehavior(t *testing.T) {
	original := &stripe.Invoice{
		Number:            "2026/00002",
		StatusTransitions: &stripe.InvoiceStatusTransitions{FinalizedAt: time.Now().Unix()},
		Lines: &stripe.InvoiceLineItemList{
			Data: []*stripe.InvoiceLineItem{
				{Description: "B2C service", Amount: 1_270_000, Quantity: 1,
					Taxes: []*stripe.InvoiceLineItemTax{{
						Amount:        270_000,
						TaxableAmount: 1_000_000,
						TaxBehavior:   stripe.InvoiceLineItemTaxTaxBehaviorInclusive,
					}}},
			},
		},
	}
	clone := invoiceAsStorno(original, time.Now())
	tax := clone.Lines.Data[0].Taxes[0]
	if tax.TaxBehavior != stripe.InvoiceLineItemTaxTaxBehaviorInclusive {
		t.Errorf("TaxBehavior = %q, want inclusive preserved", tax.TaxBehavior)
	}
	if tax.Amount != -270_000 || tax.TaxableAmount != -1_000_000 {
		t.Errorf("tax amounts = %d/%d, want sign-flipped -270000/-1000000", tax.Amount, tax.TaxableAmount)
	}
}
