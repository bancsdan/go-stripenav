package navclient

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/nav/schemas"
)

// softwareXML mirrors the on-wire <software> structure (default xmlns).
type softwareXML struct {
	XMLName        xml.Name `xml:"software"`
	ID             string   `xml:"softwareId"`
	Name           string   `xml:"softwareName"`
	Operation      string   `xml:"softwareOperation"`
	MainVersion    string   `xml:"softwareMainVersion"`
	DevName        string   `xml:"softwareDevName"`
	DevContact     string   `xml:"softwareDevContact"`
	DevCountryCode string   `xml:"softwareDevCountryCode,omitempty"`
	DevTaxNumber   string   `xml:"softwareDevTaxNumber,omitempty"`
}

func softwareToXML(s nav.Software) softwareXML {
	return softwareXML{
		ID:             s.ID,
		Name:           s.Name,
		Operation:      s.Operation,
		MainVersion:    s.MainVersion,
		DevName:        s.DevName,
		DevContact:     s.DevContact,
		DevCountryCode: s.DevCountryCode,
		DevTaxNumber:   s.DevTaxNumber,
	}
}

// commonHeaderXML renders the <common:header> block.
type commonHeaderXML struct {
	XMLName        xml.Name `xml:"common:header"`
	RequestID      string   `xml:"common:requestId"`
	Timestamp      string   `xml:"common:timestamp"`
	RequestVersion string   `xml:"common:requestVersion"`
	HeaderVersion  string   `xml:"common:headerVersion"`
}

// commonUserXML renders the <common:user> block.
type commonUserXML struct {
	XMLName          xml.Name           `xml:"common:user"`
	Login            string             `xml:"common:login"`
	PasswordHash     passwordHashXML    `xml:"common:passwordHash"`
	TaxNumber        string             `xml:"common:taxNumber"`
	RequestSignature requestSigXML      `xml:"common:requestSignature"`
}

type passwordHashXML struct {
	CryptoType string `xml:"cryptoType,attr"`
	Value      string `xml:",chardata"`
}

type requestSigXML struct {
	CryptoType string `xml:"cryptoType,attr"`
	Value      string `xml:",chardata"`
}

// formatTimestamp renders a time as the NAV-required ISO 8601 UTC with
// millisecond precision and trailing 'Z' (e.g. 2026-05-27T10:55:31.440Z).
// This is the form that appears in <common:timestamp>.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// signingTimestamp renders a time as YYYYMMDDHHMMSS (no separators, no
// fractional seconds, no zone marker). This is the form NAV concatenates
// into the SHA3-512 request signature.
func signingTimestamp(t time.Time) string {
	return t.UTC().Format("20060102150405")
}

// newRequestID returns "RID" + 12 random decimal digits.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read effectively never fails on supported platforms; fall
		// back to time-based digits if it ever does.
		return fmt.Sprintf("RID%012d", time.Now().UnixNano()%1_000_000_000_000)
	}
	n := binary.BigEndian.Uint64(b[:]) % 1_000_000_000_000
	return fmt.Sprintf("RID%012d", n)
}

// envelopeContext holds the per-request values shared across all NAV
// requests: the unique id, the timestamp, and the signature derived from
// them and the user's signKey.
type envelopeContext struct {
	RequestID string
	Timestamp string
	Signature string
}

func newEnvelopeContext(now time.Time, signKey string, ops []SignedOperation) envelopeContext {
	id := newRequestID()
	return envelopeContext{
		RequestID: id,
		Timestamp: formatTimestamp(now),
		Signature: RequestSignature(id, signingTimestamp(now), signKey, ops),
	}
}

func (ec envelopeContext) header() commonHeaderXML {
	return commonHeaderXML{
		RequestID:      ec.RequestID,
		Timestamp:      ec.Timestamp,
		RequestVersion: schemas.SchemaVersion,
		HeaderVersion:  schemas.HeaderVersion,
	}
}

func (ec envelopeContext) user(login, password, taxNumber string) commonUserXML {
	return commonUserXML{
		Login: login,
		PasswordHash: passwordHashXML{
			CryptoType: "SHA-512",
			Value:      PasswordHash(password),
		},
		TaxNumber: taxNumber,
		RequestSignature: requestSigXML{
			CryptoType: "SHA3-512",
			Value:      ec.Signature,
		},
	}
}
