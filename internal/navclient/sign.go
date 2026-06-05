package navclient

import (
	"crypto/sha512"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/sha3"
)

// SignedOperation describes a per-operation contribution to a manageInvoice
// or manageAnnulment request signature. Empty for tokenExchange and query
// operations, which sign only the request id + timestamp + signKey.
type SignedOperation struct {
	// Operation is the NAV operation code (CREATE, MODIFY, STORNO, ANNUL).
	Operation string
	// Base64Payload is the base64-encoded invoice (or annulment) XML
	// exactly as it appears in the <invoiceData>/<invoiceAnnulment>
	// element.
	Base64Payload string
}

// PasswordHash returns uppercase-hex SHA-512 of the given password,
// suitable for the <common:passwordHash> element.
func PasswordHash(password string) string {
	sum := sha512.Sum512([]byte(password))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// IndexHash returns the per-operation contribution NAV calls the
// "index hash": uppercase-hex SHA3-512 of (operation + base64Payload).
func IndexHash(operation, base64Payload string) string {
	sum := sha3.Sum512([]byte(operation + base64Payload))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// RequestSignature computes the NAV <common:requestSignature> value as
// uppercase-hex SHA3-512 of the concatenation of:
//
//   - requestID
//   - timestamp formatted as YYYYMMDDhhmmss in UTC (no separators, no
//     fractional seconds, no zone marker)
//   - the technical user's signKey
//   - each operation's IndexHash, in index order
//
// For tokenExchange and query operations, ops is empty.
func RequestSignature(requestID, timestamp, signKey string, ops []SignedOperation) string {
	var b strings.Builder
	b.WriteString(requestID)
	b.WriteString(timestamp)
	b.WriteString(signKey)
	for _, op := range ops {
		b.WriteString(IndexHash(op.Operation, op.Base64Payload))
	}
	sum := sha3.Sum512([]byte(b.String()))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
