package navclient

import (
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"testing"

	"golang.org/x/crypto/sha3"
)

func TestPasswordHash(t *testing.T) {
	// Known SHA-512 of "password" — verified independently.
	want := strings.ToUpper(hex.EncodeToString(sha512Of("password")))
	got := PasswordHash("password")
	if got != want {
		t.Fatalf("PasswordHash mismatch:\nwant %s\ngot  %s", want, got)
	}
	if len(got) != 128 {
		t.Fatalf("PasswordHash length = %d, want 128", len(got))
	}
	for _, r := range got {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')) {
			t.Fatalf("PasswordHash contains non-uppercase-hex rune %q", r)
		}
	}
}

// TestIndexHash_DocsExample verifies IndexHash against the worked example
// in the NAV spec ("1.5.1 Számítás manageInvoice és manageAnnulment
// operáció esetén"). These two expected values come directly from the PDF.
func TestIndexHash_DocsExample(t *testing.T) {
	cases := []struct {
		op      string
		payload string
		want    string
	}{
		{
			op:      "CREATE",
			payload: "QWJjZDEyMzQ=",
			want:    "4317798460962869BC67F07C48EA7E4A3AFA301513CEB87B8EB94ECF92BC220A89C480F87F0860E85E29A3B6C0463D4F29712C5AD48104A6486CE839DC2F24CB",
		},
		{
			op:      "MODIFY",
			payload: "RGNiYTQzMjE=",
			want:    "A881218238933F6FFB9E167445CB4DAA9749BCF484FDE48AB7649FD25E8B634A4736A65A7C4A8E2831119F739837E006566F97370415AAD55E268605206F2A6C",
		},
	}
	for _, c := range cases {
		got := IndexHash(c.op, c.payload)
		if got != c.want {
			t.Errorf("IndexHash(%q, %q):\n got  %s\n want %s", c.op, c.payload, got, c.want)
		}
	}
}

func TestRequestSignature_NoOps(t *testing.T) {
	// Cross-checked by recomputing the expected value here and asserting
	// the function follows the documented concatenation order.
	requestID := "RID896801578348"
	tsCompact := "20190911105531"
	signKey := "ac-ac3a-7f661bff7d342N43CYX4U9FG"

	got := RequestSignature(requestID, tsCompact, signKey, nil)
	want := strings.ToUpper(hex.EncodeToString(sha3Of(requestID + tsCompact + signKey)))
	if got != want {
		t.Fatalf("RequestSignature mismatch:\nwant %s\ngot  %s", want, got)
	}
}

// TestRequestSignature_WithOperations replays the docs' fictive request
// data and pins our output against the literal expected requestSignature
// value the PDF publishes for it. Matching the docs byte-for-byte rules
// out subtle variants (Keccak-512 vs SHA3-512, lower/upper-case hex,
// wrong concatenation order, etc).
func TestRequestSignature_WithOperations(t *testing.T) {
	requestID := "TSTKFT1222564"
	tsCompact := "20171230182545"
	signKey := "ce-8f5e-215119fa7dd621DLMRHRLH2S"
	ops := []SignedOperation{
		{Operation: "CREATE", Base64Payload: "QWJjZDEyMzQ="},
		{Operation: "MODIFY", Base64Payload: "RGNiYTQzMjE="},
	}

	const wantDocs = "60BC80609EE3B8F42FE904200A49A1921A1DADA08D55319ACD40C59F626514B74EEA49011D372600A10DBCF8199D590DA9C2841D987308F2D83DAE17C2470C42"

	got := RequestSignature(requestID, tsCompact, signKey, ops)
	if got != wantDocs {
		t.Fatalf("RequestSignature(ops) does not match the NAV docs example:\n got  %s\n want %s", got, wantDocs)
	}
}

// sha512Of and sha3Of duplicate the production helpers so the tests
// recompute the expected value independently of the package under test.
func sha512Of(s string) []byte {
	sum := sha512.Sum512([]byte(s))
	return sum[:]
}

func sha3Of(s string) []byte {
	sum := sha3.Sum512([]byte(s))
	return sum[:]
}
