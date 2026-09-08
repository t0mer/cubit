package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// TokenFileName is the encrypted session blob inside the data directory.
const TokenFileName = "token.enc"

// ErrNoSession means nothing has been persisted yet. It is an ordinary
// first-run condition, not a failure.
var ErrNoSession = errors.New("session: no persisted session")

// Persisted is what gets written to disk, encrypted.
//
// It holds cookies because the Pluxee session is a cookie rather than a bearer
// token (docs/api-notes.md §3).
type Persisted struct {
	Cookies []*http.Cookie `json:"cookies"`
	SavedAt time.Time      `json:"saved_at"`
}

// Store reads and writes the encrypted session file.
type Store struct {
	path string
	key  []byte
}

// NewStore returns a Store writing into dir. The key must be 32 bytes.
func NewStore(dir string, key []byte) (*Store, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("session: data directory must be set")
	}
	return &Store{
		path: filepath.Join(dir, TokenFileName),
		key:  append([]byte(nil), key...),
	}, nil
}

// Path returns the location of the session file, for logging.
func (s *Store) Path() string { return s.path }

// Save encrypts and atomically writes the session.
func (s *Store) Save(p *Persisted) error {
	if p == nil {
		return fmt.Errorf("session: nothing to save")
	}
	p.SavedAt = time.Now().UTC()

	plaintext, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding session: %w", err)
	}
	sealed, err := encrypt(s.key, plaintext)
	if err != nil {
		return fmt.Errorf("encrypting session: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	// Write to a temporary file and rename, so a crash mid-write cannot leave a
	// half-written session behind.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary session file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting session file permissions: %w", err)
	}
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		return fmt.Errorf("writing session file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing session file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing session file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replacing session file: %w", err)
	}
	return nil
}

// Load decrypts the persisted session. It returns ErrNoSession when none exists.
func (s *Store) Load() (*Persisted, error) {
	sealed, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("reading session file: %w", err)
	}

	plaintext, err := decrypt(s.key, sealed)
	if err != nil {
		return nil, fmt.Errorf("session file is unreadable (wrong encryption key, or corrupt): %w", err)
	}

	var p Persisted
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return nil, fmt.Errorf("decoding session: %w", err)
	}
	return &p, nil
}

// Clear removes the persisted session. Removing a session that is not there is
// not an error.
func (s *Store) Clear() error {
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing session file: %w", err)
	}
	return nil
}
