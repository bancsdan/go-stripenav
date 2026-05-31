package schemas

import "encoding/xml"

// InvoiceAnnulment is the root element of the per-annulment payload that
// is base64-encoded and embedded inside a ManageAnnulmentRequest.
type InvoiceAnnulment struct {
	XMLName             xml.Name `xml:"http://schemas.nav.gov.hu/OSA/3.0/annul InvoiceAnnulment"`
	AnnulmentReference  string   `xml:"annulmentReference"`
	AnnulmentTimestamp  string   `xml:"annulmentTimestamp"` // ISO 8601 UTC
	AnnulmentCode       string   `xml:"annulmentCode"`      // ERRATIC_DATA, ERRATIC_INVOICE_NUMBER, ERRATIC_INVOICE_ISSUE_DATE, ERRATIC_ELECTRONIC_HASH
	AnnulmentReason     string   `xml:"annulmentReason"`
}
