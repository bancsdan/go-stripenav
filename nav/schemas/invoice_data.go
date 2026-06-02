package schemas

import "encoding/xml"

// InvoiceData is the root element of the per-invoice payload that is
// base64-encoded and embedded inside a ManageInvoiceRequest operation.
//
// The struct covers the subset of NAV v3.0 fields a Stripe invoice can
// populate. Fields outside that subset (lottery indicators, fiscal
// representative, advance/aggregate types, etc.) are intentionally omitted
// — they can be added without affecting existing callers.
type InvoiceData struct {
	XMLName xml.Name `xml:"http://schemas.nav.gov.hu/OSA/3.0/data InvoiceData"`

	InvoiceNumber         string      `xml:"invoiceNumber"`
	InvoiceIssueDate      string      `xml:"invoiceIssueDate"` // YYYY-MM-DD
	CompletenessIndicator bool        `xml:"completenessIndicator"`
	InvoiceMain           InvoiceMain `xml:"invoiceMain"`
}

type InvoiceMain struct {
	Invoice Invoice `xml:"invoice"`
}

type Invoice struct {
	InvoiceReference *InvoiceReference `xml:"invoiceReference,omitempty"`
	InvoiceHead      InvoiceHead       `xml:"invoiceHead"`
	InvoiceLines     InvoiceLines      `xml:"invoiceLines"`
	InvoiceSummary   InvoiceSummary    `xml:"invoiceSummary"`
}

// InvoiceReference identifies the original invoice when this submission
// modifies or annuls it.
type InvoiceReference struct {
	OriginalInvoiceNumber string `xml:"originalInvoiceNumber"`
	ModifyWithoutMaster   bool   `xml:"modifyWithoutMaster"`
	ModificationIndex     int    `xml:"modificationIndex"`
}

type InvoiceHead struct {
	SupplierInfo  SupplierInfo  `xml:"supplierInfo"`
	CustomerInfo  *CustomerInfo `xml:"customerInfo,omitempty"`
	InvoiceDetail InvoiceDetail `xml:"invoiceDetail"`
}

type SupplierInfo struct {
	SupplierTaxNumber TaxNumberType `xml:"supplierTaxNumber"`
	SupplierName      string        `xml:"supplierName"`
	SupplierAddress   Address       `xml:"supplierAddress"`
	SupplierBank      string        `xml:"supplierBankAccountNumber,omitempty"`
}

type CustomerInfo struct {
	CustomerVatStatus string           `xml:"customerVatStatus"` // DOMESTIC, OTHER, PRIVATE_PERSON
	CustomerVatData   *CustomerVatData `xml:"customerVatData,omitempty"`
	CustomerName      string           `xml:"customerName,omitempty"`
	CustomerAddress   *Address         `xml:"customerAddress,omitempty"`
	CustomerBank      string           `xml:"customerBankAccountNumber,omitempty"`
}

type CustomerVatData struct {
	CustomerTaxNumber       *TaxNumberType         `xml:"customerTaxNumber,omitempty"`
	CommunityVatNumber      string                 `xml:"communityVatNumber,omitempty"`
	ThirdStateTaxID         string                 `xml:"thirdStateTaxId,omitempty"`
}

// TaxNumberType is the Hungarian 11-digit VAT number split into the three
// parts NAV requires. Defined in the OSA/3.0/base namespace.
type TaxNumberType struct {
	TaxpayerID string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base taxpayerId"`
	VatCode    string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base vatCode,omitempty"`
	CountyCode string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base countyCode,omitempty"`
}

// Address is a choice between simpleAddress and detailedAddress. Exactly
// one of the two pointers must be set; the marshaller will emit nothing
// for the other.
type Address struct {
	Simple   *SimpleAddress   `xml:"http://schemas.nav.gov.hu/OSA/3.0/base simpleAddress,omitempty"`
	Detailed *DetailedAddress `xml:"http://schemas.nav.gov.hu/OSA/3.0/base detailedAddress,omitempty"`
}

type SimpleAddress struct {
	CountryCode      string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base countryCode"`
	Region           string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base region,omitempty"`
	PostalCode       string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base postalCode"`
	City             string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base city"`
	AdditionalAddress string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base additionalAddressDetail"`
}

type DetailedAddress struct {
	CountryCode        string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base countryCode"`
	Region             string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base region,omitempty"`
	PostalCode         string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base postalCode"`
	City               string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base city"`
	StreetName         string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base streetName"`
	PublicPlaceCategory string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base publicPlaceCategory"`
	Number             string `xml:"http://schemas.nav.gov.hu/OSA/3.0/base number,omitempty"`
}

type InvoiceDetail struct {
	InvoiceCategory            string `xml:"invoiceCategory"` // NORMAL, SIMPLIFIED, AGGREGATE
	InvoiceDeliveryDate        string `xml:"invoiceDeliveryDate"`
	InvoiceDeliveryPeriodStart string `xml:"invoiceDeliveryPeriodStart,omitempty"`
	InvoiceDeliveryPeriodEnd   string `xml:"invoiceDeliveryPeriodEnd,omitempty"`
	// PeriodicalSettlement marks a §58 continuous-service invoice.
	// Pointer so the element is omitted entirely when unset (vs. emitting
	// the default "false" for a value type).
	PeriodicalSettlement *bool  `xml:"periodicalSettlement,omitempty"`
	CurrencyCode         string `xml:"currencyCode"` // ISO 4217
	ExchangeRate         string `xml:"exchangeRate"`
	PaymentMethod        string `xml:"paymentMethod,omitempty"` // TRANSFER, CARD, CASH, OTHER
	PaymentDate          string `xml:"paymentDate,omitempty"`
	InvoiceAppearance    string `xml:"invoiceAppearance"` // PAPER, ELECTRONIC, EDI, UNKNOWN
}

type InvoiceLines struct {
	MergedItemIndicator bool   `xml:"mergedItemIndicator"`
	Lines               []Line `xml:"line"`
}

type Line struct {
	LineNumber              int             `xml:"lineNumber"`
	LineModificationReference *LineModificationReference `xml:"lineModificationReference,omitempty"`
	LineExpressionIndicator bool            `xml:"lineExpressionIndicator"`
	LineNatureIndicator     string          `xml:"lineNatureIndicator,omitempty"` // PRODUCT, SERVICE, OTHER
	LineDescription         string          `xml:"lineDescription"`
	Quantity                string          `xml:"quantity,omitempty"`
	UnitOfMeasure           string          `xml:"unitOfMeasure,omitempty"`
	UnitOfMeasureOwn        string          `xml:"unitOfMeasureOwn,omitempty"`
	UnitPrice               string          `xml:"unitPrice,omitempty"`
	UnitPriceHUF            string          `xml:"unitPriceHUF,omitempty"`
	LineAmountsNormal       *LineAmounts    `xml:"lineAmountsNormal,omitempty"`
	LineAmountsSimplified   *LineAmountsSimplified `xml:"lineAmountsSimplified,omitempty"`
}

type LineModificationReference struct {
	LineNumberReference int    `xml:"lineNumberReference"`
	LineOperation       string `xml:"lineOperation"` // CREATE, MODIFY
}

type LineAmounts struct {
	LineNetAmountData LineNetAmount  `xml:"lineNetAmountData"`
	LineVatRate       LineVatRate    `xml:"lineVatRate"`
	LineVatData       *LineVatAmount `xml:"lineVatData,omitempty"`
	LineGrossAmountData *LineGrossAmount `xml:"lineGrossAmountData,omitempty"`
}

type LineAmountsSimplified struct {
	LineVatRate              LineVatRate `xml:"lineVatRate"`
	LineGrossAmountSimplified string     `xml:"lineGrossAmountSimplified"`
	LineGrossAmountSimplifiedHUF string  `xml:"lineGrossAmountSimplifiedHUF"`
}

type LineNetAmount struct {
	LineNetAmount    string `xml:"lineNetAmount"`
	LineNetAmountHUF string `xml:"lineNetAmountHUF"`
}

type LineVatAmount struct {
	LineVatAmount    string `xml:"lineVatAmount"`
	LineVatAmountHUF string `xml:"lineVatAmountHUF"`
}

type LineGrossAmount struct {
	LineGrossAmountNormal    string `xml:"lineGrossAmountNormal"`
	LineGrossAmountNormalHUF string `xml:"lineGrossAmountNormalHUF"`
}

type LineVatRate struct {
	VatPercentage    string  `xml:"vatPercentage,omitempty"`
	VatContent       string  `xml:"vatContent,omitempty"`
	VatExemption     *VatExemption `xml:"vatExemption,omitempty"`
	VatOutOfScope    *VatOutOfScope `xml:"vatOutOfScope,omitempty"`
	NoVatCharge      bool    `xml:"noVatCharge,omitempty"`
}

type VatExemption struct {
	Case   string `xml:"case"`
	Reason string `xml:"reason"`
}

type VatOutOfScope struct {
	Case   string `xml:"case"`
	Reason string `xml:"reason"`
}

type InvoiceSummary struct {
	SummaryNormal     *SummaryNormal     `xml:"summaryNormal,omitempty"`
	SummarySimplified []SummarySimplified `xml:"summarySimplified,omitempty"`
	SummaryGrossData  SummaryGrossData   `xml:"summaryGrossData"`
}

type SummaryNormal struct {
	SummaryByVatRate     []SummaryByVatRate `xml:"summaryByVatRate"`
	InvoiceNetAmount     string             `xml:"invoiceNetAmount"`
	InvoiceNetAmountHUF  string             `xml:"invoiceNetAmountHUF"`
	InvoiceVatAmount     string             `xml:"invoiceVatAmount"`
	InvoiceVatAmountHUF  string             `xml:"invoiceVatAmountHUF"`
}

type SummaryByVatRate struct {
	VatRate           LineVatRate         `xml:"vatRate"`
	VatRateNetData    VatRateNetData      `xml:"vatRateNetData"`
	VatRateVatData    VatRateVatData      `xml:"vatRateVatData"`
	VatRateGrossData  VatRateGrossData    `xml:"vatRateGrossData"`
}

type VatRateNetData struct {
	VatRateNetAmount    string `xml:"vatRateNetAmount"`
	VatRateNetAmountHUF string `xml:"vatRateNetAmountHUF"`
}

type VatRateVatData struct {
	VatRateVatAmount    string `xml:"vatRateVatAmount"`
	VatRateVatAmountHUF string `xml:"vatRateVatAmountHUF"`
}

type VatRateGrossData struct {
	VatRateGrossAmount    string `xml:"vatRateGrossAmount"`
	VatRateGrossAmountHUF string `xml:"vatRateGrossAmountHUF"`
}

type SummarySimplified struct {
	VatRate                  LineVatRate `xml:"vatRate"`
	VatContentGrossAmount    string      `xml:"vatContentGrossAmount"`
	VatContentGrossAmountHUF string      `xml:"vatContentGrossAmountHUF"`
}

type SummaryGrossData struct {
	InvoiceGrossAmount    string `xml:"invoiceGrossAmount"`
	InvoiceGrossAmountHUF string `xml:"invoiceGrossAmountHUF"`
}
