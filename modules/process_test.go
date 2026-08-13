package modules_test

import (
	"crypto/aes"
	"encoding/base64"
	"strings"
	"testing"

	"Pantegnos/modules"

	_ "Pantegnos/modules/impl"
)

// TestProcessNetmodRoundTrip verifies the shared Process() pipeline end-to-end
// using the NetMod format, whose decoder is AES-ECB with the fixed key
// "_netsyna_netmod_" and whose file content is base64(AES-ECB(plaintext)).
//
// NetMod's decoder does NOT PKCS7-unpad; it trims trailing NUL bytes, so the
// plaintext here is padded with NULs to a 16-byte boundary.
func TestProcessNetmodRoundTrip(t *testing.T) {
	key := []byte("_netsyna_netmod_") // 16 bytes -> AES-128

	plaintext := []byte("nm-test://example.com:443")
	// Pad with NULs to a multiple of the AES block size.
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append([]byte{}, plaintext...)
	padded = append(padded, make([]byte, pad)...)

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	ct := make([]byte, len(padded))
	for i := 0; i < len(padded); i += block.BlockSize() {
		block.Encrypt(ct[i:i+block.BlockSize()], padded[i:i+block.BlockSize()])
	}
	payload := base64.StdEncoding.EncodeToString(ct)

	out, err := modules.Process("test.nm", []byte(payload))
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	want := "nm-test://example.com:443"
	if !strings.Contains(out, want) {
		t.Fatalf("decrypted output = %q, expected it to contain %q", out, want)
	}
}
