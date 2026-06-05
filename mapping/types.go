package mapping

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
// a simpleAddress or detailedAddress; the bridge emits simpleAddress when
// only the high-level fields are populated.
type Address struct {
	CountryCode      string
	Region           string
	PostalCode       string
	City             string
	AdditionalDetail string
}
