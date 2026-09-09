// Package crypt holds the AES-256-GCM helpers used to encrypt everything cubit
// keeps on disk: the Pluxee session, and the credentials of notification
// channels. One implementation, one key length, one place to get it right.
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// KeyLength is the required encryption key length: AES-256 takes 32 bytes.
const KeyLength = 32

// ValidateKey checks that a key is usable for AES-256-GCM. Callers run this at
// startup so a bad key is a configuration error, not a first-write surprise.
func ValidateKey(key []byte) error {
	if len(key) != KeyLength {
		return fmt.Errorf("encryption key must be exactly %d bytes, got %d", KeyLength, len(key))
	}
	return nil
}

// Seal encrypts plaintext, returning nonce||ciphertext||tag.
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	// Seal appends to nonce, so the nonce prefixes the result.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal. It fails on the wrong key and on any tampering, because
// GCM authenticates the ciphertext.
func Open(key, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext is shorter than the %d-byte nonce", gcm.NonceSize())
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating gcm: %w", err)
	}
	return gcm, nil
}
