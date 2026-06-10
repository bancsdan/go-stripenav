package invoicemap

import (
	"strings"

	"github.com/bancsdan/go-stripenav/nav/schemas"
)

// CustomerVatStatus is the NAV-required category for the customer.
type CustomerVatStatus = string

// The NAV customerVatStatus literals.
const (
	CustomerDomestic      CustomerVatStatus = "DOMESTIC"
	CustomerOther         CustomerVatStatus = "OTHER"
	CustomerPrivatePerson CustomerVatStatus = "PRIVATE_PERSON"
)

// splitHungarianTaxNumber parses the 11-character Hungarian VAT number
// (with or without hyphens, with or without an "HU" prefix) into its
// taxpayerId/vatCode/countyCode parts.
//
// Returns ok=false when the input does not contain exactly 11 digits.
func splitHungarianTaxNumber(s string) (tax schemas.TaxNumberType, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.ToUpper(s), "HU")
	digits := make([]byte, 0, 11)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
		case c == '-' || c == ' ':
			continue
		default:
			return schemas.TaxNumberType{}, false
		}
	}
	if len(digits) != 11 {
		return schemas.TaxNumberType{}, false
	}
	return schemas.TaxNumberType{
		TaxpayerID: string(digits[0:8]),
		VatCode:    string(digits[8:9]),
		CountyCode: string(digits[9:11]),
	}, true
}

// taxIDLike is the minimum surface mapping needs from a Stripe tax id.
type taxIDLike struct {
	Type  string
	Value string
}

// classifyCustomer picks the NAV customerVatStatus and produces the
// matching CustomerVatData block from the supplied tax IDs and name.
//
//   - A Hungarian VAT id (hu_tin, or eu_vat HU…) maps to DOMESTIC with a
//     parsed customerTaxNumber.
//   - Any other EU VAT id maps to OTHER with communityVatNumber.
//   - A non-EU business tax id maps to OTHER with thirdStateTaxId.
//   - No id and no business name maps to PRIVATE_PERSON.
//
// Stripe customers can carry several tax IDs in arbitrary order, so
// classification is by specificity, not list position: a Hungarian id
// anywhere in the list wins over an EU id, which wins over a
// third-state id. First-match-in-order would let a leading us_ein
// shadow the hu_tin that makes the customer DOMESTIC.
func classifyCustomer(taxIDs []taxIDLike, hasBusinessName bool) (status CustomerVatStatus, vat *schemas.CustomerVatData) {
	var euVat, thirdState string
	for _, t := range taxIDs {
		typ := strings.ToLower(t.Type)
		val := strings.TrimSpace(t.Value)
		if val == "" {
			continue
		}
		switch typ {
		case "hu_tin":
			if tn, ok := splitHungarianTaxNumber(val); ok {
				return CustomerDomestic, &schemas.CustomerVatData{CustomerTaxNumber: &tn}
			}
		case "eu_vat":
			if strings.HasPrefix(strings.ToUpper(val), "HU") {
				if tn, ok := splitHungarianTaxNumber(val); ok {
					return CustomerDomestic, &schemas.CustomerVatData{CustomerTaxNumber: &tn}
				}
			}
			if euVat == "" {
				euVat = val
			}
		case "eu_oss_vat":
			if euVat == "" {
				euVat = val
			}
		default:
			// Any other id type (e.g. us_ein, ca_bn) is a third-state
			// business tax number.
			if thirdState == "" {
				thirdState = val
			}
		}
	}
	if euVat != "" {
		return CustomerOther, &schemas.CustomerVatData{CommunityVatNumber: euVat}
	}
	if thirdState != "" {
		return CustomerOther, &schemas.CustomerVatData{ThirdStateTaxID: thirdState}
	}
	if hasBusinessName {
		return CustomerOther, nil
	}
	return CustomerPrivatePerson, nil
}
