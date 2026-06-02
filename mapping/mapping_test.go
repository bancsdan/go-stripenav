package mapping

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v82"
)

func defaultSupplier() Supplier {
	return Supplier{
		TaxNumber: "12345678-9-01",
		Name:      "Test Merchant Kft.",
		Address: Address{
			CountryCode: "HU",
			PostalCode:  "1011",
			City:        "Budapest",
			AdditionalDetail: "Fő utca 1.",
		},
	}
}

// makeInvoice builds a minimal but realistic Stripe invoice for testing.
// statusTransitionsFinalized is the finalize-at epoch seconds; 0 means "not set".
func makeInvoice(number, currency string, statusTransitionsFinalized int64, lines []*stripe.InvoiceLineItem) *stripe.Invoice {
	return &stripe.Invoice{
		Number:   number,
		Currency: stripe.Currency(strings.ToLower(currency)),
		StatusTransitions: &stripe.InvoiceStatusTransitions{
			FinalizedAt: statusTransitionsFinalized,
		},
		Lines: &stripe.InvoiceLineItemList{Data: lines},
	}
}

func huLine(desc string, amount int64, vat int64) *stripe.InvoiceLineItem {
	return &stripe.InvoiceLineItem{
		Description: desc,
		Amount:      amount,
		Quantity:    1,
		Taxes: []*stripe.InvoiceLineItemTax{
			{Amount: vat},
		},
	}
}

func TestMapInvoice_HUF_SingleLine27Vat(t *testing.T) {
	inv := makeInvoice("2026/00001", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		// 10 000 HUF net with 2 700 HUF VAT (27%). Stripe stores HUF in
		// minor units (×100), so the values below are 1 000 000 and
		// 270 000 respectively.
		huLine("Service", 1_000_000, 270_000),
	})

	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}

	if got.InvoiceNumber != "2026/00001" {
		t.Errorf("invoiceNumber = %q", got.InvoiceNumber)
	}
	if got.InvoiceIssueDate == "" {
		t.Errorf("invoiceIssueDate is empty")
	}

	lines := got.InvoiceMain.Invoice.InvoiceLines.Lines
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	l := lines[0]
	if l.LineAmountsNormal.LineNetAmountData.LineNetAmount != "10000.00" {
		t.Errorf("lineNetAmount = %q", l.LineAmountsNormal.LineNetAmountData.LineNetAmount)
	}
	if l.LineAmountsNormal.LineVatData.LineVatAmount != "2700.00" {
		t.Errorf("lineVatAmount = %q", l.LineAmountsNormal.LineVatData.LineVatAmount)
	}
	if l.LineAmountsNormal.LineGrossAmountData.LineGrossAmountNormal != "12700.00" {
		t.Errorf("lineGrossAmount = %q", l.LineAmountsNormal.LineGrossAmountData.LineGrossAmountNormal)
	}
	if l.LineAmountsNormal.LineVatRate.VatPercentage != "0.27" {
		t.Errorf("vatPercentage = %q (want 0.27)", l.LineAmountsNormal.LineVatRate.VatPercentage)
	}

	summary := got.InvoiceMain.Invoice.InvoiceSummary
	if summary.SummaryGrossData.InvoiceGrossAmount != "12700.00" {
		t.Errorf("summaryGross = %q", summary.SummaryGrossData.InvoiceGrossAmount)
	}
	if len(summary.SummaryNormal.SummaryByVatRate) != 1 {
		t.Fatalf("summary rows = %d", len(summary.SummaryNormal.SummaryByVatRate))
	}
}

func TestMapInvoice_DeterministicRoundTrip(t *testing.T) {
	inv := makeInvoice("2026/00001", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 1_000_000, 270_000),
	})
	a, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatal(err)
	}
	aXML, _ := xml.Marshal(a)
	bXML, _ := xml.Marshal(b)
	if string(aXML) != string(bXML) {
		t.Fatalf("MapInvoice not deterministic")
	}
}

func TestMapInvoice_MixedVatRates(t *testing.T) {
	inv := makeInvoice("2026/00002", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Standard", 1_000_000, 270_000), // 27%
		huLine("Reduced", 500_000, 25_000),     // 5%
	})
	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	rows := got.InvoiceMain.Invoice.InvoiceSummary.SummaryNormal.SummaryByVatRate
	if len(rows) != 2 {
		t.Fatalf("expected 2 vat-rate rows, got %d", len(rows))
	}
}

func TestMapInvoice_NonHUF_RequiresExchangeRate(t *testing.T) {
	inv := makeInvoice("2026/00003", "eur", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 10_000, 2_700),
	})
	_, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	var me *MappingError
	if !errors.As(err, &me) || me.Code != CodeMissingExchangeRate {
		t.Fatalf("want MISSING_EXCHANGE_RATE_FOR_NON_HUF_INVOICE, got %v", err)
	}
}

func TestMapInvoice_EUR_WithExchangeRate(t *testing.T) {
	inv := makeInvoice("2026/00004", "eur", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 10_000, 2_700), // 100 EUR + 27 EUR
	})
	got, err := MapInvoice(inv, MapOptions{
		Supplier:          defaultSupplier(),
		ExchangeRateToHUF: "400",
	})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	if got.InvoiceMain.Invoice.InvoiceSummary.SummaryGrossData.InvoiceGrossAmount != "127.00" {
		t.Errorf("foreign gross = %q", got.InvoiceMain.Invoice.InvoiceSummary.SummaryGrossData.InvoiceGrossAmount)
	}
	if got.InvoiceMain.Invoice.InvoiceSummary.SummaryGrossData.InvoiceGrossAmountHUF != "50800" {
		t.Errorf("HUF gross = %q want 50800", got.InvoiceMain.Invoice.InvoiceSummary.SummaryGrossData.InvoiceGrossAmountHUF)
	}
}

func TestMapInvoice_PrivatePerson(t *testing.T) {
	inv := makeInvoice("2026/00005", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 1_000_000, 270_000),
	})
	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	if got.InvoiceMain.Invoice.InvoiceHead.CustomerInfo.CustomerVatStatus != CustomerPrivatePerson {
		t.Errorf("expected PRIVATE_PERSON, got %s", got.InvoiceMain.Invoice.InvoiceHead.CustomerInfo.CustomerVatStatus)
	}
}

func TestMapInvoice_EUVatCustomer(t *testing.T) {
	inv := makeInvoice("2026/00006", "eur", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 10_000, 0),
	})
	taxType := stripe.TaxIDType("eu_vat")
	inv.CustomerTaxIDs = []*stripe.InvoiceCustomerTaxID{
		{Type: &taxType, Value: "DE123456789"},
	}
	inv.CustomerName = "ACME GmbH"
	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier(), ExchangeRateToHUF: "400"})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	c := got.InvoiceMain.Invoice.InvoiceHead.CustomerInfo
	if c.CustomerVatStatus != CustomerOther || c.CustomerVatData == nil || c.CustomerVatData.CommunityVatNumber != "DE123456789" {
		t.Fatalf("want OTHER + DE123456789, got %+v", c)
	}
}

func TestMapInvoice_ErrorCases(t *testing.T) {
	good := makeInvoice("2026/x", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{huLine("x", 100, 27)})

	cases := []struct {
		name string
		mut  func(*stripe.Invoice, *MapOptions)
		code string
	}{
		{"missing supplier tax", func(_ *stripe.Invoice, o *MapOptions) { o.Supplier.TaxNumber = "" }, CodeSupplierTaxNumberRequired},
		{"bad supplier tax", func(_ *stripe.Invoice, o *MapOptions) { o.Supplier.TaxNumber = "not-a-number" }, CodeSupplierTaxNumberRequired},
		{"no lines", func(i *stripe.Invoice, _ *MapOptions) { i.Lines = &stripe.InvoiceLineItemList{} }, CodeInvoiceLinesEmpty},
		{"no number", func(i *stripe.Invoice, _ *MapOptions) { i.Number = "" }, CodeInvoiceNumberMissing},
		{"no issue date", func(i *stripe.Invoice, _ *MapOptions) {
			i.StatusTransitions = nil
			i.EffectiveAt = 0
			i.Created = 0
		}, CodeIssueDateMissing},
		{"modify without origin", func(_ *stripe.Invoice, o *MapOptions) {
			o.Operation = OpModify
		}, CodeModificationMissingOrigin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv := *good
			lines := make([]*stripe.InvoiceLineItem, len(good.Lines.Data))
			copy(lines, good.Lines.Data)
			inv.Lines = &stripe.InvoiceLineItemList{Data: lines}
			if good.StatusTransitions != nil {
				st := *good.StatusTransitions
				inv.StatusTransitions = &st
			}
			opts := MapOptions{Supplier: defaultSupplier()}
			c.mut(&inv, &opts)
			_, err := MapInvoice(&inv, opts)
			var me *MappingError
			if !errors.As(err, &me) || me.Code != c.code {
				t.Fatalf("want code %s, got %v", c.code, err)
			}
		})
	}
}

func TestMapInvoice_ModifyOperation(t *testing.T) {
	inv := makeInvoice("2026/00007-M1", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Adjustment", 100_000, 27_000),
	})
	got, err := MapInvoice(inv, MapOptions{
		Supplier:              defaultSupplier(),
		Operation:             OpModify,
		OriginalInvoiceNumber: "2026/00007",
		ModificationIndex:     1,
	})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	ref := got.InvoiceMain.Invoice.InvoiceReference
	if ref == nil || ref.OriginalInvoiceNumber != "2026/00007" || ref.ModificationIndex != 1 {
		t.Fatalf("invoiceReference = %+v", ref)
	}
}

func TestMapInvoice_SubscriptionAdvanceBilling(t *testing.T) {
	// Monthly subscription invoice: finalized & paid 2026-01-01, covers
	// the 2026-01-01 → 2026-01-31 service period. Under §58 advance-billing
	// the tax point equals the invoice issue date.
	inv := makeInvoice("2026/00010", "huf", 1_767_225_600, []*stripe.InvoiceLineItem{
		huLine("Monthly plan", 1_000_000, 270_000),
	})
	inv.BillingReason = stripe.InvoiceBillingReasonSubscriptionCycle
	inv.PeriodStart = 1_767_225_600 // 2026-01-01 UTC
	inv.PeriodEnd = 1_769_817_600   // 2026-01-31 UTC

	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	d := got.InvoiceMain.Invoice.InvoiceHead.InvoiceDetail
	if d.PeriodicalSettlement == nil || !*d.PeriodicalSettlement {
		t.Errorf("PeriodicalSettlement = %v, want true", d.PeriodicalSettlement)
	}
	if d.InvoiceDeliveryPeriodStart != "2026-01-01" {
		t.Errorf("InvoiceDeliveryPeriodStart = %q", d.InvoiceDeliveryPeriodStart)
	}
	if d.InvoiceDeliveryPeriodEnd != "2026-01-31" {
		t.Errorf("InvoiceDeliveryPeriodEnd = %q", d.InvoiceDeliveryPeriodEnd)
	}
	// §58 advance-billing: tax point = issue date.
	if d.InvoiceDeliveryDate != "2026-01-01" {
		t.Errorf("InvoiceDeliveryDate = %q, want issue date 2026-01-01", d.InvoiceDeliveryDate)
	}
	// CARD fallback: no due_date → payment date = issue date.
	if d.PaymentDate != "2026-01-01" {
		t.Errorf("PaymentDate = %q, want 2026-01-01", d.PaymentDate)
	}
}

func TestMapInvoice_OneOffNoPeriodicalSettlement(t *testing.T) {
	inv := makeInvoice("2026/00011", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 1_000_000, 270_000),
	})
	// One-off / manual invoice: no subscription, no period.
	inv.BillingReason = stripe.InvoiceBillingReasonManual

	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	d := got.InvoiceMain.Invoice.InvoiceHead.InvoiceDetail
	if d.PeriodicalSettlement != nil {
		t.Errorf("PeriodicalSettlement = %v, want nil", *d.PeriodicalSettlement)
	}
	if d.InvoiceDeliveryPeriodStart != "" || d.InvoiceDeliveryPeriodEnd != "" {
		t.Errorf("period fields populated on one-off: start=%q end=%q",
			d.InvoiceDeliveryPeriodStart, d.InvoiceDeliveryPeriodEnd)
	}
}

func TestMapInvoice_PaymentDateFallback(t *testing.T) {
	inv := makeInvoice("2026/00012", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 1_000_000, 270_000),
	})
	// No DueDate, default PaymentMethod (CARD).
	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	d := got.InvoiceMain.Invoice.InvoiceHead.InvoiceDetail
	if d.PaymentDate == "" || d.PaymentDate != d.InvoiceDeliveryDate {
		t.Errorf("PaymentDate = %q, want = InvoiceDeliveryDate %q",
			d.PaymentDate, d.InvoiceDeliveryDate)
	}

	// Non-CARD payment with no due date → no synthesis.
	inv2 := makeInvoice("2026/00013", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 1_000_000, 270_000),
	})
	got2, err := MapInvoice(inv2, MapOptions{
		Supplier:      defaultSupplier(),
		PaymentMethod: "TRANSFER",
	})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	if d2 := got2.InvoiceMain.Invoice.InvoiceHead.InvoiceDetail; d2.PaymentDate != "" {
		t.Errorf("non-CARD PaymentDate = %q, want empty", d2.PaymentDate)
	}
}

func TestMapInvoice_MarshalsToXML(t *testing.T) {
	inv := makeInvoice("2026/00008", "huf", 1_700_000_000, []*stripe.InvoiceLineItem{
		huLine("Service", 1_000_000, 270_000),
	})
	got, err := MapInvoice(inv, MapOptions{Supplier: defaultSupplier()})
	if err != nil {
		t.Fatalf("MapInvoice: %v", err)
	}
	out, err := xml.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("xml.Marshal: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`xmlns="http://schemas.nav.gov.hu/OSA/3.0/data"`,
		`<invoiceNumber>2026/00008</invoiceNumber>`,
		`<completenessIndicator>false</completenessIndicator>`,
		`xmlns="http://schemas.nav.gov.hu/OSA/3.0/base"`, // base namespace appears on taxNumber children
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshalled XML missing %q\n---\n%s", want, s)
		}
	}
}
