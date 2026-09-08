package session

import (
	"bytes"
	"strings"
	"testing"
)

func testKey(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b + byte(i)
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey(1)
	plain := []byte(`{"cookies":[{"name":"token","value":"sekrit"}]}`)

	sealed, err := encrypt(key, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(sealed, []byte("sekrit")) {
		t.Fatal("ciphertext contains the plaintext secret")
	}

	got, err := decrypt(key, sealed)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("decrypt = %q, want %q", got, plain)
	}
}

func TestEncryptIsNonDeterministic(t *testing.T) {
	key := testKey(2)
	a, err := encrypt(key, []byte("same plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := encrypt(key, []byte("same plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext are identical; the nonce is not random")
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	sealed, err := encrypt(testKey(3), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decrypt(testKey(9), sealed); err == nil {
		t.Fatal("decrypt with the wrong key succeeded")
	}
}

func TestDecryptDetectsTampering(t *testing.T) {
	key := testKey(4)
	sealed, err := encrypt(key, []byte("secret payload here"))
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 0xff

	if _, err := decrypt(key, sealed); err == nil {
		t.Fatal("decrypt accepted a tampered ciphertext")
	}
}

func TestDecryptRejectsTruncatedCiphertext(t *testing.T) {
	if _, err := decrypt(testKey(5), []byte("short")); err == nil {
		t.Fatal("decrypt accepted a ciphertext shorter than the nonce")
	}
}

func TestValidateKeyRequires32Bytes(t *testing.T) {
	for _, n := range []int{0, 1, 16, 31, 33, 64} {
		if err := ValidateKey(make([]byte, n)); err == nil {
			t.Errorf("ValidateKey with %d bytes: want error, got nil", n)
		}
	}
	if err := ValidateKey(make([]byte, 32)); err != nil {
		t.Errorf("ValidateKey with 32 bytes: %v", err)
	}
}

func TestValidateKeyErrorMentionsRequiredLength(t *testing.T) {
	err := ValidateKey(make([]byte, 8))
	if err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("error %v should tell the operator the key must be 32 bytes", err)
	}
}
