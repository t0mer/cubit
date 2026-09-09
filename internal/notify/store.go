package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/t0mer/cubit/internal/crypt"
)

// ChannelsFileName is the encrypted channel list inside the data directory.
const ChannelsFileName = "channels.enc"

// ErrNotFound means no channel has the given id.
var ErrNotFound = errors.New("notify: no such channel")

// persisted is the on-disk shape, encrypted with the same AES-256-GCM key as
// the Pluxee session. It holds provider tokens, so it never touches disk in
// plaintext.
type persisted struct {
	Channels []Channel `json:"channels"`
	SavedAt  time.Time `json:"saved_at"`
}

// Store reads and writes the encrypted channel list.
type Store struct {
	mu   sync.Mutex
	path string
	key  []byte
}

// NewStore returns a Store writing into dir. The key must be 32 bytes.
func NewStore(dir string, key []byte) (*Store, error) {
	if err := crypt.ValidateKey(key); err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("notify: data directory must be set")
	}
	return &Store{
		path: filepath.Join(dir, ChannelsFileName),
		key:  append([]byte(nil), key...),
	}, nil
}

// Path returns the location of the channel file, for logging.
func (s *Store) Path() string { return s.path }

// List returns every configured channel, credentials included. Callers exposing
// these over the API must redact them first.
func (s *Store) List() ([]Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	return p.Channels, nil
}

// Add validates, assigns an id and persists.
func (s *Store) Add(ch Channel) (Channel, error) {
	ch.Normalise()
	if err := ch.Validate(); err != nil {
		return Channel{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.loadLocked()
	if err != nil {
		return Channel{}, err
	}
	ch.ID = uuid.NewString()
	p.Channels = append(p.Channels, ch)
	if err := s.saveLocked(p); err != nil {
		return Channel{}, err
	}
	return ch, nil
}

// Update replaces the channel with the same id.
func (s *Store) Update(ch Channel) error {
	ch.Normalise()
	if err := ch.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range p.Channels {
		if p.Channels[i].ID == ch.ID {
			p.Channels[i] = ch
			return s.saveLocked(p)
		}
	}
	return ErrNotFound
}

// Delete removes a channel.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range p.Channels {
		if p.Channels[i].ID == id {
			p.Channels = append(p.Channels[:i], p.Channels[i+1:]...)
			return s.saveLocked(p)
		}
	}
	return ErrNotFound
}

// Get returns one channel by id.
func (s *Store) Get(id string) (Channel, error) {
	channels, err := s.List()
	if err != nil {
		return Channel{}, err
	}
	for _, c := range channels {
		if c.ID == id {
			return c, nil
		}
	}
	return Channel{}, ErrNotFound
}

// loadLocked reads the file. A missing file is an ordinary first-run state.
func (s *Store) loadLocked() (*persisted, error) {
	sealed, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &persisted{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.path, err)
	}
	plaintext, err := crypt.Open(s.key, sealed)
	if err != nil {
		return nil, fmt.Errorf("decrypting %s: %w", s.path, err)
	}
	var p persisted
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return nil, fmt.Errorf("decoding channels: %w", err)
	}
	return &p, nil
}

// saveLocked encrypts and atomically replaces the file.
func (s *Store) saveLocked(p *persisted) error {
	p.SavedAt = time.Now().UTC()
	plaintext, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding channels: %w", err)
	}
	sealed, err := crypt.Seal(s.key, plaintext)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating the data directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replacing %s: %w", s.path, err)
	}
	return nil
}
