package session

import "github.com/t0mer/cubit/internal/crypt"

// The session file and the notification channel file are encrypted the same
// way, so the implementation lives in internal/crypt. These are thin aliases
// kept for the package's own readability.

// KeyLength is the required encryption key length: AES-256 takes 32 bytes.
const KeyLength = crypt.KeyLength

// ValidateKey checks that a key is usable for AES-256-GCM.
func ValidateKey(key []byte) error { return crypt.ValidateKey(key) }

func encrypt(key, plaintext []byte) ([]byte, error) { return crypt.Seal(key, plaintext) }

func decrypt(key, sealed []byte) ([]byte, error) { return crypt.Open(key, sealed) }
