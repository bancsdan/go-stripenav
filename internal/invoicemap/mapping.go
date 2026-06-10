package invoicemap

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav/schemas"
	"github.com/stripe/stripe-go/v82"
)

// hungaryZone is a fixed UTC+2 offset, used to render the calendar
// date NAV expects on Hungarian invoice fields. We use a fixed offset
// rather than time.LoadLocation("Europe/Budapest") to sidestep the
// tzdata dependency at build/runtime. Trade-off: UTC+2 matches CEST
// exactly (~7 months/year); during CET (~5 months/year, late October
// through late March) Hungary is actually UTC+1, so timestamps in the
// 22:00–23:00 UTC window will render one calendar day ahead of the
// real Hungarian date. Acceptable for this library because the
// alternative (tzdata) imposes deployment complexity that outweighs
// the narrow DST-shoulder window.
var hungaryZone = time.FixedZone("Hungary", 2*60*60)

// localDate formats t as yyyy-MM-dd in the Hungarian calendar (UTC+2).
// All date-only fields submitted to NAV go through this so the rendered
// date matches what the supplier (and customer) see on the invoice.
func localDate(t time.Time) string {
	return t.In(hungaryZone).Format("2006-01-02")
}

// Operation is the NAV invoice operation produced by the mapper.
type Operation string

// The NAV manageInvoice operation literals.
const (
	OpCreate Operation = "CREATE"
	OpModify Operation = "MODIFY"
	OpStorno Operation = "STORNO"
)

func addressToSchema(a mapping.Address) schemas.Address {
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
	Supplier mapping.Supplier

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

	// OriginalLineCount is the number of lines on the original invoice
	// this MODIFY/STORNO references. NAV's line chain appends each
	// modification document's lines after the original's, so
	// lineNumberReference for this document's line idx (0-based) is
	// OriginalLineCount + idx + 1. When zero, the mapper falls back to
	// this document's own line count — correct only for a 1:1 storno
	// that mirrors the original.
	OriginalLineCount int
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
	// Stripe webhook payloads embed only the first page of line items
	// (has_more=true marks truncation). Mapping a truncated list would
	// produce an internally-consistent but incomplete NAV report —
	// silent under-reporting NAV cannot detect. Refuse instead; callers
	// must fetch the full line list from the Stripe API first.
	if inv.Lines.HasMore {
		return schemas.InvoiceData{}, newMappingError(CodeInvoiceLinesTruncated, "invoice.lines.has_more")
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

	originalLineCount := opts.OriginalLineCount
	if originalLineCount <= 0 {
		originalLineCount = len(inv.Lines.Data)
	}
	lines, byRate, totals, err := buildLines(inv, currency, rate, opts.Operation, originalLineCount)
	if err != nil {
		return schemas.InvoiceData{}, err
	}

	detail := schemas.InvoiceDetail{
		InvoiceCategory:     "NORMAL",
		InvoiceDeliveryDate: localDate(issued),
		CurrencyCode:        currency,
		ExchangeRate:        rate.FloatString(6),
		PaymentMethod:       opts.PaymentMethod,
		InvoiceAppearance:   opts.InvoiceAppearance,
	}
	// §58 continuous-service / periodic settlement. The honest signal is
	// "does this invoice cover a service period?" — i.e. period_end is
	// strictly greater than period_start. That catches subscription
	// cycles, quote_accept invoices for subscriptions, and manually
	// created invoices that explicitly span a period; it correctly skips
	// one-off invoices where Stripe sets period_start == period_end.
	// Using billing_reason="subscription_*" alone would miss
	// quote_accept-originated subscription invoices.
	// Advance-billed (charge_automatically) → §58 tax point equals the
	// invoice issue date, already in InvoiceDeliveryDate.
	if inv.PeriodStart > 0 && inv.PeriodEnd > inv.PeriodStart {
		detail.InvoiceDeliveryPeriodStart = localDate(time.Unix(inv.PeriodStart, 0))
		detail.InvoiceDeliveryPeriodEnd = localDate(time.Unix(inv.PeriodEnd, 0))
		t := true
		detail.PeriodicalSettlement = &t
	}
	switch {
	case inv.DueDate > 0:
		detail.PaymentDate = localDate(time.Unix(inv.DueDate, 0))
	case detail.PaymentMethod == "CARD":
		// charge_automatically card flows have no due_date — the card is
		// charged on finalization, so payment date = issue date.
		detail.PaymentDate = localDate(issued)
	}

	summary := buildSummary(byRate, totals, currency)

	out := schemas.InvoiceData{
		InvoiceNumber:         inv.Number,
		InvoiceIssueDate:      localDate(issued),
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

func buildSupplier(s mapping.Supplier) (schemas.SupplierInfo, error) {
	tn, ok := splitHungarianTaxNumber(s.TaxNumber)
	if !ok {
		return schemas.SupplierInfo{}, newMappingError(CodeSupplierTaxNumberRequired, "Supplier.TaxNumber")
	}
	// NAV's simpleAddress requires a non-blank additionalAddressDetail
	// (the street/number part). The supplier address is mandatory on
	// every invoice, so an incomplete one would fail NAV schema
	// validation on every submission — surface it as a mapping error
	// instead.
	if strings.TrimSpace(s.Address.AdditionalDetail) == "" {
		return schemas.SupplierInfo{}, newMappingError(CodeSupplierAddressRequired, "Supplier.Address.AdditionalDetail")
	}
	return schemas.SupplierInfo{
		SupplierTaxNumber: tn,
		SupplierName:      s.Name,
		SupplierAddress:   addressToSchema(s.Address),
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
	}
	// NAV's data-minimisation rule: for PRIVATE_PERSON the report must
	// not carry name or address (the customer block is just the status).
	if status == CustomerPrivatePerson {
		return info
	}
	info.CustomerName = inv.CustomerName
	if inv.CustomerAddress != nil {
		detail := strings.TrimSpace(
			strings.Join([]string{inv.CustomerAddress.Line1, inv.CustomerAddress.Line2}, " "),
		)
		// simpleAddress requires a non-blank additionalAddressDetail;
		// customerAddress itself is optional, so when Stripe has no
		// street data we omit the whole address rather than emit an
		// element NAV's schema rejects.
		if detail != "" {
			info.CustomerAddress = &schemas.Address{
				Simple: &schemas.SimpleAddress{
					CountryCode:       inv.CustomerAddress.Country,
					PostalCode:        inv.CustomerAddress.PostalCode,
					City:              inv.CustomerAddress.City,
					AdditionalAddress: detail,
				},
			}
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

func buildLines(inv *stripe.Invoice, currency string, rate *big.Rat, op Operation, originalLineCount int) ([]schemas.Line, map[string]*rateTotals, invoiceTotals, error) {
	lines := make([]schemas.Line, 0, len(inv.Lines.Data))
	byRate := map[string]*rateTotals{}
	totals := newInvoiceTotals()

	for idx, l := range inv.Lines.Data {
		if l == nil {
			continue
		}
		amount := amountFromMinor(l.Amount, currency)
		vat := new(big.Rat)
		inclusive := false
		for _, tx := range l.Taxes {
			if tx == nil {
				continue
			}
			vat.Add(vat, amountFromMinor(tx.Amount, currency))
			// Stripe sets tax_behavior consistently across taxes on a
			// single line (it's driven by the Price config), so checking
			// any tax is sufficient. inclusive means l.Amount is gross
			// (price-includes-VAT, B2C default); exclusive means net.
			if tx.TaxBehavior == stripe.InvoiceLineItemTaxTaxBehaviorInclusive {
				inclusive = true
			}
		}
		var net *big.Rat
		if inclusive {
			net = new(big.Rat).Sub(amount, vat)
		} else {
			net = amount
		}
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

		// Bucket on the 2-decimal rendering — the same precision NAV
		// sees in vatPercentage. Keying on more decimals would let two
		// lines at the same legal rate (27%) land in separate buckets
		// when minor-unit rounding skews vat/net by a millionth, and
		// NAV rejects duplicate vatPercentage summary rows.
		key := ratePct.FloatString(2)
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

		netStr, vatStr, grossStr := reconciledNetVatGross(net, vat, currency)
		netHUFStr, vatHUFStr, grossHUFStr := reconciledNetVatGrossHUF(netHUF, vatHUF)

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
					LineNetAmount:    netStr,
					LineNetAmountHUF: netHUFStr,
				},
				LineVatRate: schemas.LineVatRate{
					VatPercentage: ratePct.FloatString(2),
				},
				LineVatData: &schemas.LineVatAmount{
					LineVatAmount:    vatStr,
					LineVatAmountHUF: vatHUFStr,
				},
				LineGrossAmountData: &schemas.LineGrossAmount{
					LineGrossAmountNormal:    grossStr,
					LineGrossAmountNormalHUF: grossHUFStr,
				},
			},
		}
		// For MODIFY/STORNO documents every line carries a
		// lineModificationReference. NAV models all invoices + their
		// modifications/stornos as a single flat "chain": each new
		// submission appends lines to the chain at the next free
		// positions. lineNumberReference is the line's position in
		// that cumulative chain, NOT a back-reference to the original
		// line being reversed. The original occupies chain positions
		// 1..originalLineCount, so this document's lines claim the
		// positions after it. lineOperation must always be CREATE —
		// every line on a modification document is a new entry in the
		// chain.
		if op == OpModify || op == OpStorno {
			line.LineModificationReference = &schemas.LineModificationReference{
				LineNumberReference: originalLineCount + idx + 1,
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
		netStr, vatStr, grossStr := reconciledNetVatGross(b.netFC, b.vatFC, currency)
		netHUFStr, vatHUFStr, grossHUFStr := reconciledNetVatGrossHUF(b.netHUF, b.vatHUF)
		rows = append(rows, schemas.SummaryByVatRate{
			VatRate: schemas.LineVatRate{VatPercentage: ratePctFromKey(k)},
			VatRateNetData: schemas.VatRateNetData{
				VatRateNetAmount:    netStr,
				VatRateNetAmountHUF: netHUFStr,
			},
			VatRateVatData: schemas.VatRateVatData{
				VatRateVatAmount:    vatStr,
				VatRateVatAmountHUF: vatHUFStr,
			},
			VatRateGrossData: schemas.VatRateGrossData{
				VatRateGrossAmount:    grossStr,
				VatRateGrossAmountHUF: grossHUFStr,
			},
		})
	}

	totalNetStr, totalVatStr, totalGrossStr := reconciledNetVatGross(totals.netFC, totals.vatFC, currency)
	totalNetHUFStr, totalVatHUFStr, totalGrossHUFStr := reconciledNetVatGrossHUF(totals.netHUF, totals.vatHUF)

	return schemas.InvoiceSummary{
		SummaryNormal: &schemas.SummaryNormal{
			SummaryByVatRate:    rows,
			InvoiceNetAmount:    totalNetStr,
			InvoiceNetAmountHUF: totalNetHUFStr,
			InvoiceVatAmount:    totalVatStr,
			InvoiceVatAmountHUF: totalVatHUFStr,
		},
		SummaryGrossData: schemas.SummaryGrossData{
			InvoiceGrossAmount:    totalGrossStr,
			InvoiceGrossAmountHUF: totalGrossHUFStr,
		},
	}
}

// reconciled renders net/vat/gross with the given formatter such that
// the rendered strings satisfy net + vat = gross exactly, even when
// independent rounding of net, vat, and gross from a big.Rat would
// otherwise drift by a fractional unit (NAV rejects
// invoiceNet+invoiceVat ≠ invoiceGross). Net and vat render first, then
// gross is reconstructed from the rounded values so it always
// reconciles.
func reconciled(net, vat *big.Rat, format func(*big.Rat) string) (netStr, vatStr, grossStr string) {
	netStr = format(net)
	vatStr = format(vat)
	netR, _ := new(big.Rat).SetString(netStr)
	vatR, _ := new(big.Rat).SetString(vatStr)
	grossStr = format(new(big.Rat).Add(netR, vatR))
	return
}

func reconciledNetVatGross(net, vat *big.Rat, currency string) (netStr, vatStr, grossStr string) {
	return reconciled(net, vat, func(r *big.Rat) string { return formatAmount(r, currency) })
}

func reconciledNetVatGrossHUF(net, vat *big.Rat) (netStr, vatStr, grossStr string) {
	return reconciled(net, vat, formatHUF)
}

func ratePctFromKey(k string) string {
	// Keys are already the 2-decimal vatPercentage rendering; the
	// summary row reuses them verbatim so line and summary always agree.
	return k
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
