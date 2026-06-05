package nav

// Software identifies the calling application as required by NAV's
// "software" block. All fields are mandatory in the v3.0 envelope.
type Software struct {
	ID             string // softwareId — 18 chars
	Name           string
	Operation      string // LOCAL_SOFTWARE or ONLINE_SERVICE
	MainVersion    string
	DevName        string
	DevContact     string
	DevCountryCode string // ISO 3166-1 alpha-2, e.g. HU
	DevTaxNumber   string
}

// InvoiceOperation is one operation in a manageInvoice batch — the unit
// stripenav.NAVClient.SubmitInvoice consumes.
type InvoiceOperation struct {
	// Operation is CREATE, MODIFY or STORNO.
	Operation string
	// InvoiceData is the marshalled, base64-unencoded XML for one
	// InvoiceData document.
	InvoiceData []byte
}

// AnnulmentOperation is one operation in a manageAnnulment batch — the
// unit stripenav.NAVClient.AnnulInvoice consumes.
type AnnulmentOperation struct {
	// InvoiceAnnulment is the marshalled, base64-unencoded XML for one
	// InvoiceAnnulment document.
	InvoiceAnnulment []byte
}

// SubmitResult is what the NAV client returns for a successful
// submission to manageInvoice or manageAnnulment.
type SubmitResult struct {
	TransactionID string
}
