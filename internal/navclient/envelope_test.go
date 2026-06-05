package navclient

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/bancsdan/go-stripenav/nav"
)

func TestFormatTimestamp(t *testing.T) {
	tt := time.Date(2019, 9, 11, 10, 55, 31, 440_000_000, time.UTC)
	if got := formatTimestamp(tt); got != "2019-09-11T10:55:31.440Z" {
		t.Fatalf("formatTimestamp = %q", got)
	}
	if got := signingTimestamp(tt); got != "20190911105531" {
		t.Fatalf("signingTimestamp = %q", got)
	}
}

func TestNewRequestID(t *testing.T) {
	id := newRequestID()
	if !strings.HasPrefix(id, "RID") || len(id) != 15 {
		t.Fatalf("newRequestID = %q (want RID + 12 digits)", id)
	}
	for _, r := range id[3:] {
		if r < '0' || r > '9' {
			t.Fatalf("newRequestID has non-digit rune: %q", r)
		}
	}
	// Two consecutive calls should not collide.
	if newRequestID() == id {
		t.Fatalf("newRequestID returned the same id twice: %q", id)
	}
}

func TestTokenExchangeRequest_OnWire(t *testing.T) {
	now := time.Date(2026, 5, 27, 22, 0, 0, 0, time.UTC)
	ec := newEnvelopeContext(now, "signkey", nil)
	req := tokenExchangeRequest{
		XmlnsCom: xmlnsCommonURI,
		Header:   ec.header(),
		User:     ec.user("login", "password", "11111111"),
		Software: softwareToXML(nav.Software{ID: "SW01", Name: "n", Operation: "LOCAL_SOFTWARE", MainVersion: "v"}),
	}
	body, err := xml.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(body)
	// Must declare both namespaces (common via xmlns:common attr, default
	// via the request's xmlns attr derived from the root XMLName).
	for _, want := range []string{
		`xmlns:common="http://schemas.nav.gov.hu/NTCA/1.0/common"`,
		`xmlns="http://schemas.nav.gov.hu/OSA/3.0/api"`,
		`<common:requestId>` + ec.RequestID + `</common:requestId>`,
		`<common:timestamp>2026-05-27T22:00:00.000Z</common:timestamp>`,
		`<common:requestVersion>3.0</common:requestVersion>`,
		`<common:headerVersion>1.0</common:headerVersion>`,
		`<common:login>login</common:login>`,
		`<common:passwordHash cryptoType="SHA-512">`,
		`<common:taxNumber>11111111</common:taxNumber>`,
		`<common:requestSignature cryptoType="SHA3-512">` + ec.Signature + `</common:requestSignature>`,
		`<softwareId>SW01</softwareId>`,
		`<softwareOperation>LOCAL_SOFTWARE</softwareOperation>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshalled request missing %q\n--- body ---\n%s", want, s)
		}
	}
}
