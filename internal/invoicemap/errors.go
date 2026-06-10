package invoicemap

import "fmt"

// MappingError codes. Use these constants to branch on failure mode
// instead of comparing strings.
const (
	CodeSupplierTaxNumberRequired = "SUPPLIER_TAX_NUMBER_REQUIRED"
	CodeSupplierAddressRequired   = "SUPPLIER_ADDRESS_REQUIRED"
	CodeInvoiceLinesEmpty         = "INVOICE_LINES_EMPTY"
	CodeInvoiceLinesTruncated     = "INVOICE_LINES_TRUNCATED"
	CodeInvoiceNumberMissing      = "INVOICE_NUMBER_MISSING"
	CodeIssueDateMissing          = "ISSUE_DATE_MISSING"
	CodeUnsupportedCurrency       = "UNSUPPORTED_CURRENCY"
	CodeLineAmountOverflow        = "LINE_AMOUNT_OVERFLOW"
	CodeMissingExchangeRate       = "MISSING_EXCHANGE_RATE_FOR_NON_HUF_INVOICE"
	CodeUnknownOperation          = "UNKNOWN_OPERATION"
	CodeModificationMissingOrigin = "MODIFICATION_MISSING_ORIGINAL_INVOICE"
)

// MappingError is returned by MapInvoice when a Stripe invoice cannot be
// translated into a valid NAV InvoiceData document.
type MappingError struct {
	Code  string
	Field string
	Cause error
}

func (e *MappingError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Field != "" {
		if e.Cause != nil {
			return fmt.Sprintf("mapping: %s at %s: %v", e.Code, e.Field, e.Cause)
		}
		return fmt.Sprintf("mapping: %s at %s", e.Code, e.Field)
	}
	if e.Cause != nil {
		return fmt.Sprintf("mapping: %s: %v", e.Code, e.Cause)
	}
	return fmt.Sprintf("mapping: %s", e.Code)
}

func (e *MappingError) Unwrap() error { return e.Cause }

func newMappingError(code, field string) *MappingError {
	return &MappingError{Code: code, Field: field}
}

func wrapMappingError(code, field string, cause error) *MappingError {
	return &MappingError{Code: code, Field: field, Cause: cause}
}
