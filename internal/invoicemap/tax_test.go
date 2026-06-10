package invoicemap

import "testing"

func TestSplitHungarianTaxNumber(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want [3]string
	}{
		{"12345678-9-01", true, [3]string{"12345678", "9", "01"}},
		{"12345678901", true, [3]string{"12345678", "9", "01"}},
		{"HU12345678901", true, [3]string{"12345678", "9", "01"}},
		{" hu 12345678-9-01 ", true, [3]string{"12345678", "9", "01"}},
		{"1234567890", false, [3]string{}},
		{"12345678-9-0X", false, [3]string{}},
		{"", false, [3]string{}},
	}
	for _, c := range cases {
		got, ok := splitHungarianTaxNumber(c.in)
		if ok != c.ok {
			t.Errorf("splitHungarianTaxNumber(%q) ok=%v want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.TaxpayerID != c.want[0] || got.VatCode != c.want[1] || got.CountyCode != c.want[2] {
			t.Errorf("splitHungarianTaxNumber(%q) = %+v want %v", c.in, got, c.want)
		}
	}
}

func TestClassifyCustomer(t *testing.T) {
	t.Run("hungarian VAT id", func(t *testing.T) {
		status, vat := classifyCustomer([]taxIDLike{{Type: "hu_tin", Value: "12345678-9-01"}}, true)
		if status != CustomerDomestic {
			t.Fatalf("want DOMESTIC, got %v", status)
		}
		if vat == nil || vat.CustomerTaxNumber == nil || vat.CustomerTaxNumber.TaxpayerID != "12345678" {
			t.Fatalf("vat data missing: %+v", vat)
		}
	})
	t.Run("EU non-HU VAT id", func(t *testing.T) {
		status, vat := classifyCustomer([]taxIDLike{{Type: "eu_vat", Value: "DE123456789"}}, true)
		if status != CustomerOther {
			t.Fatalf("want OTHER, got %v", status)
		}
		if vat == nil || vat.CommunityVatNumber != "DE123456789" {
			t.Fatalf("expected communityVatNumber DE123456789, got %+v", vat)
		}
	})
	t.Run("EU HU VAT id treated as domestic", func(t *testing.T) {
		status, vat := classifyCustomer([]taxIDLike{{Type: "eu_vat", Value: "HU12345678901"}}, true)
		if status != CustomerDomestic || vat == nil || vat.CustomerTaxNumber == nil {
			t.Fatalf("expected DOMESTIC with taxNumber, got status=%v vat=%+v", status, vat)
		}
	})
	t.Run("non-EU business id", func(t *testing.T) {
		status, vat := classifyCustomer([]taxIDLike{{Type: "us_ein", Value: "12-3456789"}}, true)
		if status != CustomerOther || vat == nil || vat.ThirdStateTaxID != "12-3456789" {
			t.Fatalf("expected OTHER with thirdStateTaxId, got status=%v vat=%+v", status, vat)
		}
	})
	t.Run("private person", func(t *testing.T) {
		status, vat := classifyCustomer(nil, false)
		if status != CustomerPrivatePerson {
			t.Fatalf("want PRIVATE_PERSON, got %v", status)
		}
		if vat != nil {
			t.Fatalf("private person should have nil vat data, got %+v", vat)
		}
	})
	t.Run("hungarian id wins regardless of list position", func(t *testing.T) {
		status, vat := classifyCustomer([]taxIDLike{
			{Type: "us_ein", Value: "12-3456789"},
			{Type: "eu_vat", Value: "DE123456789"},
			{Type: "hu_tin", Value: "12345678-9-01"},
		}, true)
		if status != CustomerDomestic || vat == nil || vat.CustomerTaxNumber == nil {
			t.Fatalf("expected DOMESTIC (hu_tin should outrank earlier ids), got status=%v vat=%+v", status, vat)
		}
	})
	t.Run("EU id wins over earlier third-state id", func(t *testing.T) {
		status, vat := classifyCustomer([]taxIDLike{
			{Type: "us_ein", Value: "12-3456789"},
			{Type: "eu_vat", Value: "DE123456789"},
		}, true)
		if status != CustomerOther || vat == nil || vat.CommunityVatNumber != "DE123456789" {
			t.Fatalf("expected OTHER with communityVatNumber (EU outranks third-state), got status=%v vat=%+v", status, vat)
		}
	})
}
