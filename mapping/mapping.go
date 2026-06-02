package mapping

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/bancsdan/go-stripenav/nav/schemas"
	"github.com/stripe/stripe-go/v82"
)

// Operation is the NAV invoice operation produced by the mapper.
type Operation string

const (
	OpCreate Operation = "CREATE"
	OpModify Operation = "MODIFY"
	OpStorno Operation = "STORNO"
)

// Supplier identifies the merchant issuing the invoice. Required for
// every CREATE: NAV will reject the submission if any field is missing.
type Supplier struct {
	// TaxNumber is the 11-character Hungarian VAT number, with or
	// without hyphens. Example: "12345678-9-01".
	TaxNumber string
	// Name is the legal/registered name of the supplier.
	Name string
	// Address is the supplier's registered address.
	Address Address
	// BankAccount is optional and shows up on the invoice for the
	// customer's reference.
	BankAccount string
}

// Address is the merchant-facing simple address shape. NAV accepts either
// a simpleAddress or detailedAddress; the mapper emits simpleAddress when
// only the high-level fields are populated.
type Address struct {
	CountryCode      string
	Region           string
	PostalCode       string
	City             string
	AdditionalDetail string
}

func (a Address) toSchema() schemas.Address {
	return schemas.Address{
		Simple: &schemas.SimpleAddress{
			CountryCode:       a.CountryCode,
			Region:            a.Region,
			PostalCode:        a.PostalCode,
			City:              a.City,
			AdditionalAddress: a.AdditionalDetail,
		},
	}
}

// MapOptions configures a single MapInvoice call.
type MapOptions struct {
	// Supplier identifies the merchant. Required.
	Supplier Supplier

	// Operation is CREATE, MODIFY or STORNO. Defaults to CREATE.
	Operation Operation

	// OriginalInvoiceNumber is required when Operation is MODIFY or
	// STORNO; it names the prior invoice this submission references.
	OriginalInvoiceNumber string

	// ModificationIndex is required when Operation is MODIFY: it is the
	// sequence number of this modification (1, 2, 3, …) against the
	// original invoice.
	ModificationIndex int

	// ExchangeRateToHUF is the rate used to convert the invoice currency
	// into HUF for the summary fields. Required when the invoice
	// currency is not HUF. Expressed as "1 unit foreign = rate HUF" and
	// passed as a decimal string (e.g. "395.42").
	ExchangeRateToHUF string

	// InvoiceAppearance is one of PAPER, ELECTRONIC, EDI, UNKNOWN.
	// Defaults to ELECTRONIC, which is the correct value for Stripe.
	InvoiceAppearance string

	// PaymentMethod is one of TRANSFER, CARD, CASH, OTHER. Defaults to
	// CARD (Stripe's most common collection method).
	PaymentMethod string
}

// MapInvoice converts a Stripe invoice into a NAV InvoiceData document.
// The function is pure: no network or filesystem access. Given the same
// inputs it always returns the same output.
func MapInvoice(inv *stripe.Invoice, opts MapOptions) (schemas.InvoiceData, error) {
	if inv == nil {
		return schemas.InvoiceData{}, newMappingError("INVOICE_NIL", "invoice")
	}
	if err := validateOptions(&opts); err != nil {
		return schemas.InvoiceData{}, err
	}
	if inv.Number == "" {
		return schemas.InvoiceData{}, newMappingError(CodeInvoiceNumberMissing, "invoice.number")
	}
	issued := issueDate(inv)
	if issued.IsZero() {
		return schemas.InvoiceData{}, newMappingError(CodeIssueDateMissing, "invoice.status_transitions.finalized_at")
	}
	if inv.Lines == nil || len(inv.Lines.Data) == 0 {
		return schemas.InvoiceData{}, newMappingError(CodeInvoiceLinesEmpty, "invoice.lines")
	}

	currency := strings.ToUpper(string(inv.Currency))
	if currency == "" {
		return schemas.InvoiceData{}, newMappingError(CodeUnsupportedCurrency, "invoice.currency")
	}
	rate := big.NewRat(1, 1)
	if currency != "HUF" {
		if opts.ExchangeRateToHUF == "" {
			return schemas.InvoiceData{}, newMappingError(CodeMissingExchangeRate, "MapOptions.ExchangeRateToHUF")
		}
		r, err := parseRate(opts.ExchangeRateToHUF)
		if err != nil {
			return schemas.InvoiceData{}, wrapMappingError(CodeMissingExchangeRate, "MapOptions.ExchangeRateToHUF", err)
		}
		rate = r
	}

	supplierInfo, err := buildSupplier(opts.Supplier)
	if err != nil {
		return schemas.InvoiceData{}, err
	}
	customerInfo := buildCustomer(inv)

	lines, byRate, totals, err := buildLines(inv, currency, rate, opts.Operation)
	if err != nil {
		return schemas.InvoiceData{}, err
	}

	detail := schemas.InvoiceDetail{
		InvoiceCategory:     "NORMAL",
		InvoiceDeliveryDate: issued.Format("2006-01-02"),
		CurrencyCode:        currency,
		ExchangeRate:        rate.FloatString(6),
		PaymentMethod:       opts.PaymentMethod,
		InvoiceAppearance:   opts.InvoiceAppearance,
	}
	// §58 continuous-service / periodic settlement. Stripe billing_reason
	// values starting with "subscription_" cover the cycle/create/update/
	// threshold cases — all of them have populated period_start/period_end
	// describing the service window. We're advance-billing the upcoming
	// period (Stripe's default for charge_automatically), so the §58 tax
	// point equals the invoice issue date (already in InvoiceDeliveryDate).
	if strings.HasPrefix(string(inv.BillingReason), "subscription_") &&
		inv.PeriodStart > 0 && inv.PeriodEnd > 0 {
		detail.InvoiceDeliveryPeriodStart = time.Unix(inv.PeriodStart, 0).UTC().Format("2006-01-02")
		detail.InvoiceDeliveryPeriodEnd = time.Unix(inv.PeriodEnd, 0).UTC().Format("2006-01-02")
		t := true
		detail.PeriodicalSettlement = &t
	}
	switch {
	case inv.DueDate > 0:
		detail.PaymentDate = time.Unix(inv.DueDate, 0).UTC().Format("2006-01-02")
	case detail.PaymentMethod == "CARD":
		// charge_automatically card flows have no due_date — the card is
		// charged on finalization, so payment date = issue date.
		detail.PaymentDate = issued.Format("2006-01-02")
	}

	summary := buildSummary(byRate, totals, currency)

	out := schemas.InvoiceData{
		InvoiceNumber:         inv.Number,
		InvoiceIssueDate:      issued.Format("2006-01-02"),
		CompletenessIndicator: false,
		InvoiceMain: schemas.InvoiceMain{
			Invoice: schemas.Invoice{
				InvoiceHead: schemas.InvoiceHead{
					SupplierInfo:  supplierInfo,
					CustomerInfo:  customerInfo,
					InvoiceDetail: detail,
				},
				InvoiceLines: schemas.InvoiceLines{
					MergedItemIndicator: false,
					Lines:               lines,
				},
				InvoiceSummary: summary,
			},
		},
	}

	if opts.Operation == OpModify || opts.Operation == OpStorno {
		out.InvoiceMain.Invoice.InvoiceReference = &schemas.InvoiceReference{
			OriginalInvoiceNumber: opts.OriginalInvoiceNumber,
			ModificationIndex:     opts.ModificationIndex,
		}
	}

	return out, nil
}

func validateOptions(o *MapOptions) error {
	if o.Operation == "" {
		o.Operation = OpCreate
	}
	switch o.Operation {
	case OpCreate:
		// nothing more required
	case OpModify, OpStorno:
		if o.OriginalInvoiceNumber == "" {
			return newMappingError(CodeModificationMissingOrigin, "MapOptions.OriginalInvoiceNumber")
		}
		// NAV's <modificationIndex> is constrained to >= 1
		// (InvoiceUnboundedIndexType). The first modification/storno
		// against a given invoice is index 1, the second 2, etc. If
		// the caller does not track prior modifications, default to 1.
		if o.ModificationIndex <= 0 {
			o.ModificationIndex = 1
		}
	default:
		return newMappingError(CodeUnknownOperation, fmt.Sprintf("MapOptions.Operation=%q", o.Operation))
	}
	if o.InvoiceAppearance == "" {
		o.InvoiceAppearance = "ELECTRONIC"
	}
	if o.PaymentMethod == "" {
		o.PaymentMethod = "CARD"
	}
	if o.Supplier.TaxNumber == "" {
		return newMappingError(CodeSupplierTaxNumberRequired, "MapOptions.Supplier.TaxNumber")
	}
	return nil
}

func issueDate(inv *stripe.Invoice) time.Time {
	if inv.StatusTransitions != nil && inv.StatusTransitions.FinalizedAt > 0 {
		return time.Unix(inv.StatusTransitions.FinalizedAt, 0).UTC()
	}
	if inv.EffectiveAt > 0 {
		return time.Unix(inv.EffectiveAt, 0).UTC()
	}
	if inv.Created > 0 {
		return time.Unix(inv.Created, 0).UTC()
	}
	return time.Time{}
}

func buildSupplier(s Supplier) (schemas.SupplierInfo, error) {
	tn, ok := splitHungarianTaxNumber(s.TaxNumber)
	if !ok {
		return schemas.SupplierInfo{}, newMappingError(CodeSupplierTaxNumberRequired, "Supplier.TaxNumber")
	}
	return schemas.SupplierInfo{
		SupplierTaxNumber: tn,
		SupplierName:      s.Name,
		SupplierAddress:   s.Address.toSchema(),
		SupplierBank:      s.BankAccount,
	}, nil
}

func buildCustomer(inv *stripe.Invoice) *schemas.CustomerInfo {
	taxIDs := make([]taxIDLike, 0, len(inv.CustomerTaxIDs))
	for _, t := range inv.CustomerTaxIDs {
		if t == nil || t.Type == nil {
			continue
		}
		taxIDs = append(taxIDs, taxIDLike{Type: string(*t.Type), Value: t.Value})
	}
	hasBizName := inv.CustomerName != "" && (len(taxIDs) > 0 || looksLikeBusiness(inv.CustomerName))
	status, vatData := classifyCustomer(taxIDs, hasBizName)

	info := &schemas.CustomerInfo{
		CustomerVatStatus: status,
		CustomerVatData:   vatData,
		CustomerName:      inv.CustomerName,
	}
	if inv.CustomerAddress != nil {
		info.CustomerAddress = &schemas.Address{
			Simple: &schemas.SimpleAddress{
				CountryCode: inv.CustomerAddress.Country,
				PostalCode:  inv.CustomerAddress.PostalCode,
				City:        inv.CustomerAddress.City,
				AdditionalAddress: strings.TrimSpace(
					strings.Join([]string{inv.CustomerAddress.Line1, inv.CustomerAddress.Line2}, " "),
				),
			},
		}
	}
	return info
}

// looksLikeBusiness is a lightweight heuristic so we don't misclassify a
// named business with no tax id as a private individual.
func looksLikeBusiness(name string) bool {
	suffixes := []string{
		" kft.", " kft", " zrt.", " zrt", " nyrt.", " nyrt",
		" bt.", " bt", " kkt.", " kkt",
		" gmbh", " ag", " sa", " s.a.", " sas", " s.r.l.", " srl",
		" inc", " ltd", " llc", " corp", " co", " co.", " plc",
	}
	n := strings.ToLower(name)
	for _, s := range suffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// rateTotals accumulates per-VAT-rate net/vat/gross amounts as we iterate
// the Stripe line items.
type rateTotals struct {
	rateKey string // e.g. "0.270000"
	netFC   *big.Rat
	vatFC   *big.Rat
	netHUF  *big.Rat
	vatHUF  *big.Rat
}

func newRateTotals(key string) *rateTotals {
	return &rateTotals{rateKey: key, netFC: new(big.Rat), vatFC: new(big.Rat), netHUF: new(big.Rat), vatHUF: new(big.Rat)}
}

// invoiceTotals tracks running gross totals so they match the per-rate
// breakdown to the cent.
type invoiceTotals struct {
	netFC, vatFC, netHUF, vatHUF *big.Rat
}

func newInvoiceTotals() invoiceTotals {
	return invoiceTotals{netFC: new(big.Rat), vatFC: new(big.Rat), netHUF: new(big.Rat), vatHUF: new(big.Rat)}
}

func buildLines(inv *stripe.Invoice, currency string, rate *big.Rat, op Operation) ([]schemas.Line, map[string]*rateTotals, invoiceTotals, error) {
	lines := make([]schemas.Line, 0, len(inv.Lines.Data))
	byRate := map[string]*rateTotals{}
	totals := newInvoiceTotals()

	for idx, l := range inv.Lines.Data {
		if l == nil {
			continue
		}
		net := amountFromMinor(l.Amount, currency)
		vat := new(big.Rat)
		for _, tx := range l.Taxes {
			if tx == nil {
				continue
			}
			vat.Add(vat, amountFromMinor(tx.Amount, currency))
		}
		gross := new(big.Rat).Add(net, vat)
		// NAV needs the effective rate for the line. Stripe v82 only
		// exposes the tax rate ID on InvoiceLineItemTax, so we derive
		// the percentage from the amounts. If net is zero, we fall back
		// to a zero rate.
		ratePct := new(big.Rat)
		if net.Sign() != 0 {
			ratePct.Quo(vat, net)
		}
		netHUF := toHUF(net, rate)
		vatHUF := toHUF(vat, rate)

		key := ratePct.FloatString(6)
		bucket, ok := byRate[key]
		if !ok {
			bucket = newRateTotals(key)
			byRate[key] = bucket
		}
		bucket.netFC.Add(bucket.netFC, net)
		bucket.vatFC.Add(bucket.vatFC, vat)
		bucket.netHUF.Add(bucket.netHUF, netHUF)
		bucket.vatHUF.Add(bucket.vatHUF, vatHUF)

		totals.netFC.Add(totals.netFC, net)
		totals.vatFC.Add(totals.vatFC, vat)
		totals.netHUF.Add(totals.netHUF, netHUF)
		totals.vatHUF.Add(totals.vatHUF, vatHUF)

		description := l.Description
		if description == "" {
			description = fmt.Sprintf("Item %d", idx+1)
		}

		line := schemas.Line{
			LineNumber:              idx + 1,
			LineExpressionIndicator: false,
			LineNatureIndicator:     "SERVICE",
			LineDescription:         description,
			Quantity:                fmt.Sprintf("%d", maxInt64(1, l.Quantity)),
			UnitOfMeasure:           "PIECE",
			UnitPrice:               formatAmount(new(big.Rat).Quo(net, big.NewRat(maxInt64(1, l.Quantity), 1)), currency),
			UnitPriceHUF:            formatHUF(new(big.Rat).Quo(netHUF, big.NewRat(maxInt64(1, l.Quantity), 1))),
			LineAmountsNormal: &schemas.LineAmounts{
				LineNetAmountData: schemas.LineNetAmount{
					LineNetAmount:    formatAmount(net, currency),
					LineNetAmountHUF: formatHUF(netHUF),
				},
				LineVatRate: schemas.LineVatRate{
					VatPercentage: ratePct.FloatString(2),
				},
				LineVatData: &schemas.LineVatAmount{
					LineVatAmount:    formatAmount(vat, currency),
					LineVatAmountHUF: formatHUF(vatHUF),
				},
				LineGrossAmountData: &schemas.LineGrossAmount{
					LineGrossAmountNormal:    formatAmount(gross, currency),
					LineGrossAmountNormalHUF: formatHUF(toHUF(gross, rate)),
				},
			},
		}
		// For MODIFY/STORNO documents every line carries a
		// lineModificationReference. NAV models all invoices + their
		// modifications/stornos as a single flat "chain": each new
		// submission appends lines to the chain at the next free
		// positions. lineNumberReference is the line's position in
		// that cumulative chain, NOT a back-reference to the original
		// line being reversed. The original already occupies chain
		// positions 1..N, so a full storno that mirrors the original
		// 1:1 claims positions N+1..2N. lineOperation must always be
		// CREATE — every line on a modification document is a new
		// entry in the chain.
		if op == OpModify || op == OpStorno {
			line.LineModificationReference = &schemas.LineModificationReference{
				LineNumberReference: len(inv.Lines.Data) + idx + 1,
				LineOperation:       "CREATE",
			}
		}
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return nil, nil, totals, newMappingError(CodeInvoiceLinesEmpty, "invoice.lines")
	}
	return lines, byRate, totals, nil
}

func buildSummary(byRate map[string]*rateTotals, totals invoiceTotals, currency string) schemas.InvoiceSummary {
	keys := make([]string, 0, len(byRate))
	for k := range byRate {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]schemas.SummaryByVatRate, 0, len(keys))
	for _, k := range keys {
		b := byRate[k]
		gross := new(big.Rat).Add(b.netFC, b.vatFC)
		grossHUF := new(big.Rat).Add(b.netHUF, b.vatHUF)
		rows = append(rows, schemas.SummaryByVatRate{
			VatRate: schemas.LineVatRate{VatPercentage: ratePctFromKey(k)},
			VatRateNetData: schemas.VatRateNetData{
				VatRateNetAmount:    formatAmount(b.netFC, currency),
				VatRateNetAmountHUF: formatHUF(b.netHUF),
			},
			VatRateVatData: schemas.VatRateVatData{
				VatRateVatAmount:    formatAmount(b.vatFC, currency),
				VatRateVatAmountHUF: formatHUF(b.vatHUF),
			},
			VatRateGrossData: schemas.VatRateGrossData{
				VatRateGrossAmount:    formatAmount(gross, currency),
				VatRateGrossAmountHUF: formatHUF(grossHUF),
			},
		})
	}

	grossFC := new(big.Rat).Add(totals.netFC, totals.vatFC)
	grossHUF := new(big.Rat).Add(totals.netHUF, totals.vatHUF)

	return schemas.InvoiceSummary{
		SummaryNormal: &schemas.SummaryNormal{
			SummaryByVatRate:    rows,
			InvoiceNetAmount:    formatAmount(totals.netFC, currency),
			InvoiceNetAmountHUF: formatHUF(totals.netHUF),
			InvoiceVatAmount:    formatAmount(totals.vatFC, currency),
			InvoiceVatAmountHUF: formatHUF(totals.vatHUF),
		},
		SummaryGrossData: schemas.SummaryGrossData{
			InvoiceGrossAmount:    formatAmount(grossFC, currency),
			InvoiceGrossAmountHUF: formatHUF(grossHUF),
		},
	}
}

func ratePctFromKey(k string) string {
	// keys are produced via FloatString(6); render at 2 decimals for the
	// summary to match the line.vatPercentage rendering.
	r := new(big.Rat)
	if _, ok := r.SetString(k); !ok {
		return k
	}
	return r.FloatString(2)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
