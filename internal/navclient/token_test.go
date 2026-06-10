package navclient

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"strings"
	"testing"
)

// encryptForTest is the inverse of decryptExchangeToken: PKCS#7-pad,
// AES-128-ECB encrypt, base64-encode. Used to build fixtures that
// faithfully mimic what NAV puts on the wire.
func encryptForTest(t *testing.T, plain, key []byte) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	padLen := aes.BlockSize - (len(plain) % aes.BlockSize)
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(padLen)}, padLen)...)
	enc := make([]byte, len(padded))
	for i := 0; i < len(padded); i += aes.BlockSize {
		block.Encrypt(enc[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(enc)
}

func TestDecryptExchangeToken_RoundTrip(t *testing.T) {
	key := []byte("0123456789ABCDEF") // 16 bytes => AES-128
	cases := []string{
		"short",
		"exactly-16-bytes", // exactly one block — PKCS#7 still adds a full block
		"74ec2947-23a0-4730-b428-62f8d1f8e0ca5EE6BOUQ8Q21", // 48 chars, the NAV token shape we saw in production logs
	}
	for _, plain := range cases {
		t.Run(plain, func(t *testing.T) {
			encoded := encryptForTest(t, []byte(plain), key)
			got, err := decryptExchangeToken(encoded, key)
			if err != nil {
				t.Fatalf("decryptExchangeToken: %v", err)
			}
			if got != plain {
				t.Fatalf("plain mismatch: want %q got %q", plain, got)
			}
		})
	}
}

func TestDecryptExchangeToken_BadKeyLength(t *testing.T) {
	_, err := decryptExchangeToken("AAAA", []byte("short"))
	if err == nil || !strings.Contains(err.Error(), "16 bytes") {
		t.Fatalf("expected key-length error, got %v", err)
	}
}

func TestDecryptExchangeToken_NotBlockMultiple(t *testing.T) {
	key := []byte("0123456789ABCDEF")
	// 15 bytes of ciphertext is not a multiple of the AES block size.
	bad := base64.StdEncoding.EncodeToString(make([]byte, 15))
	_, err := decryptExchangeToken(bad, key)
	if err == nil {
		t.Fatalf("expected block-size error, got nil")
	}
}
