package nav

import (
	"crypto/aes"
	"encoding/base64"
	"errors"
	"fmt"
)

// decryptExchangeToken decrypts the base64-encoded token returned by
// /tokenExchange using AES-128-ECB with the user's exchangeKey, then
// strips PKCS#7 padding so the result matches NAV's schema constraint
// (SimpleText50NotBlankType, maxLength=50).
//
// The NAV spec mandates AES-128-ECB. ECB is normally unsafe, but the
// plaintext token is short-lived (5 minutes), high-entropy, and never
// reused, which removes the pattern-leak attack ECB is criticised for.
func decryptExchangeToken(encoded string, exchangeKey []byte) (string, error) {
	if len(exchangeKey) != 16 {
		return "", fmt.Errorf("nav: exchangeKey must be 16 bytes (AES-128), got %d", len(exchangeKey))
	}
	ct, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("nav: decode encodedExchangeToken: %w", err)
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("nav: encodedExchangeToken is not a multiple of the AES block size")
	}
	block, err := aes.NewCipher(exchangeKey)
	if err != nil {
		return "", fmt.Errorf("nav: aes cipher: %w", err)
	}
	pt := make([]byte, len(ct))
	for i := 0; i < len(ct); i += aes.BlockSize {
		block.Decrypt(pt[i:i+aes.BlockSize], ct[i:i+aes.BlockSize])
	}
	unpadded, err := unpadPKCS7(pt, aes.BlockSize)
	if err != nil {
		return "", fmt.Errorf("nav: unpad exchangeToken: %w", err)
	}
	return string(unpadded), nil
}

// unpadPKCS7 strips PKCS#7 padding from b. The last byte indicates how
// many bytes of padding were appended; all of those bytes must equal
// that value.
func unpadPKCS7(b []byte, blockSize int) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty plaintext")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > blockSize || n > len(b) {
		return nil, fmt.Errorf("invalid padding length %d", n)
	}
	for i := len(b) - n; i < len(b); i++ {
		if int(b[i]) != n {
			return nil, errors.New("invalid PKCS#7 padding bytes")
		}
	}
	return b[:len(b)-n], nil
}
