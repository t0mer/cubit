package session

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, testKey(7))
	if err != nil {
		t.Fatal(err)
	}

	want := &Persisted{Cookies: []*http.Cookie{
		{Name: "token", Value: "sekrit", Domain: "pluxee.co.il", Path: "/"},
	}}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Cookies) != 1 || got.Cookies[0].Value != "sekrit" {
		t.Fatalf("Load returned %+v", got.Cookies)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt was not stamped on save")
	}
}

func TestStoreWritesCiphertextNotPlaintext(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, testKey(8))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&Persisted{Cookies: []*http.Cookie{{Name: "token", Value: "sekrit"}}}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, TokenFileName))
	if err != nil {
		t.Fatalf("reading token file: %v", err)
	}
	if string(raw) == "" {
		t.Fatal("token file is empty")
	}
	for _, leak := range []string{"sekrit", "token", "cookies"} {
		if contains(raw, leak) {
			t.Errorf("token file on disk contains plaintext %q", leak)
		}
	}
}

func TestStoreFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, testKey(9))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&Persisted{}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, TokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("token file mode is %o, want no group or world access", perm)
	}
}

func TestLoadMissingFileReportsNoSession(t *testing.T) {
	s, err := NewStore(t.TempDir(), testKey(10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Load on empty dir = %v, want ErrNoSession", err)
	}
}

func TestLoadWithWrongKeyFails(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, testKey(11))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&Persisted{Cookies: []*http.Cookie{{Name: "t", Value: "v"}}}); err != nil {
		t.Fatal(err)
	}

	other, err := NewStore(dir, testKey(99))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Load(); err == nil {
		t.Fatal("Load with the wrong key succeeded")
	}
}

func TestClearRemovesTheSession(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, testKey(12))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&Persisted{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Load after Clear = %v, want ErrNoSession", err)
	}
	if err := s.Clear(); err != nil {
		t.Errorf("Clear on an already-cleared store should be a no-op, got %v", err)
	}
}

func TestNewStoreRejectsBadKey(t *testing.T) {
	if _, err := NewStore(t.TempDir(), make([]byte, 16)); err == nil {
		t.Fatal("NewStore accepted a 16-byte key")
	}
}

func contains(haystack []byte, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		bytesIndex(haystack, needle) >= 0
}

func bytesIndex(h []byte, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if string(h[i:i+len(n)]) == n {
			return i
		}
	}
	return -1
}
