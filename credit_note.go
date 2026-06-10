package stripenav

import (
	"time"

	"github.com/bancsdan/go-stripenav/nav/schemas"
	"github.com/stripe/stripe-go/v82"
)

// invoiceAsStorno clones the Stripe invoice into the shape MapInvoice
// needs for a STORNO operation: a new invoice number (so NAV does not
// reject the document as a duplicate of the original), an issue date
// set to `now` (the storno is a NEW invoice with its own issue date —
// reusing the original's finalize date misrepresents the storno's
// issuance moment per Áfa tv. §169), and sign-flipped line amounts so
// the resulting InvoiceData carries the negative totals NAV expects
// for a reversing invoice.
func invoiceAsStorno(inv *stripe.Invoice, now time.Time) *stripe.Invoice {
	clone := *inv
	clone.Number = inv.Number + "-STORNO"
	clone.StatusTransitions = &stripe.InvoiceStatusTransitions{
		FinalizedAt: now.Unix(),
	}
	if inv.Lines != nil {
		out := make([]*stripe.InvoiceLineItem, 0, len(inv.Lines.Data))
		for _, l := range inv.Lines.Data {
			if l == nil {
				continue
			}
			taxes := make([]*stripe.InvoiceLineItemTax, 0, len(l.Taxes))
			for _, t := range l.Taxes {
				if t == nil {
					continue
				}
				// TaxBehavior must survive the flip: it tells the mapper
				// whether the (negated) line amount is gross or net. An
				// inclusive-tax storno without it would compute net from
				// gross and misstate the reversal by the VAT amount.
				taxes = append(taxes, &stripe.InvoiceLineItemTax{
					Amount:        -t.Amount,
					TaxableAmount: -t.TaxableAmount,
					TaxBehavior:   t.TaxBehavior,
				})
			}
			out = append(out, &stripe.InvoiceLineItem{
				Description: l.Description,
				Amount:      -l.Amount,
				Quantity:    l.Quantity,
				Taxes:       taxes,
			})
		}
		clone.Lines = &stripe.InvoiceLineItemList{Data: out}
	}
	return &clone
}

// buildAnnulmentRecord builds a NAV InvoiceAnnulment payload for the
// given previously-reported invoice number. Used by
// (*BridgeHandler).AnnulInvoice for the out-of-band annulment flow.
func buildAnnulmentRecord(invoiceNumber, reason string, now time.Time) schemas.InvoiceAnnulment {
	return schemas.InvoiceAnnulment{
		AnnulmentReference: invoiceNumber,
		AnnulmentTimestamp: now.UTC().Format("2006-01-02T15:04:05.000Z"),
		AnnulmentCode:      "ERRATIC_DATA",
		AnnulmentReason:    reason,
	}
}

// creditNoteAsInvoice synthesises the minimum Stripe Invoice shape the
// mapper needs so credit note → InvoiceData MODIFY can reuse the same
// code path as invoice CREATE.
func creditNoteAsInvoice(cn *stripe.CreditNote) *stripe.Invoice {
	inv := &stripe.Invoice{
		Number:   cn.Number,
		Currency: cn.Currency,
		Created:  cn.Created,
		StatusTransitions: &stripe.InvoiceStatusTransitions{
			FinalizedAt: cn.EffectiveAt,
		},
		Lines: &stripe.InvoiceLineItemList{
			Data: make([]*stripe.InvoiceLineItem, 0),
		},
	}
	if cn.Lines != nil {
		for _, l := range cn.Lines.Data {
			if l == nil {
				continue
			}
			taxes := make([]*stripe.InvoiceLineItemTax, 0, len(l.Taxes))
			for _, ta := range l.Taxes {
				if ta == nil {
					continue
				}
				// Tax amounts share the line's sign; if we flip the
				// amount we must flip the tax too, otherwise the derived
				// VAT rate (vat/net) ends up negative and NAV rejects.
				taxes = append(taxes, &stripe.InvoiceLineItemTax{
					Amount:        -ta.Amount,
					TaxableAmount: -ta.TaxableAmount,
					TaxBehavior:   stripe.InvoiceLineItemTaxTaxBehavior(ta.TaxBehavior),
				})
			}
			inv.Lines.Data = append(inv.Lines.Data, &stripe.InvoiceLineItem{
				Description: l.Description,
				Amount:      -l.Amount, // MODIFY uses signed amounts
				Quantity:    l.Quantity,
				Taxes:       taxes,
			})
		}
	}
	if cn.Customer != nil {
		inv.CustomerName = cn.Customer.Name
	}
	return inv
}
