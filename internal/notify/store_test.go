package notify

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	return key
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestEmptyStoreListsNothing(t *testing.T) {
	s, _ := newStore(t)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d channels from a fresh store", len(got))
	}
}

func TestAddAssignsAnIDAndPersists(t *testing.T) {
	s, dir := newStore(t)

	ch, err := s.Add(Channel{
		Name: "whatsapp", Provider: ProviderGreenAPI,
		GreenAPI: GreenAPIConfig{InstanceID: "7103", Token: "tok", Phone: "972501234567"},
		Enabled:  true, NotifyOnSuccess: true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if ch.ID == "" {
		t.Error("Add did not assign an id")
	}

	// A second store over the same directory must see it: the point of the file.
	again, err := NewStore(dir, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "whatsapp" {
		t.Fatalf("List = %+v, want the channel just added", got)
	}
	if got[0].GreenAPI.Token != "tok" {
		t.Errorf("token did not survive the round trip")
	}
}

// The file holds provider tokens, so it must be unreadable without the key.
func TestChannelsAreEncryptedOnDisk(t *testing.T) {
	s, dir := newStore(t)
	const secret = "super-secret-green-api-token"
	if _, err := s.Add(Channel{
		Name: "wa", Provider: ProviderGreenAPI,
		GreenAPI: GreenAPIConfig{InstanceID: "1", Token: secret, Phone: "9725"},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ChannelsFileName))
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Error("the provider token is on disk in plaintext")
	}
	if strings.Contains(string(raw), "wa") {
		t.Error("the file is not encrypted; channel names are readable")
	}
}

func TestWrongKeyCannotRead(t *testing.T) {
	s, dir := newStore(t)
	if _, err := s.Add(Channel{Name: "x", Provider: ProviderShoutrrr,
		Shoutrrr: ShoutrrrConfig{URL: "gotify://host/token"}}); err != nil {
		t.Fatal(err)
	}

	other := make([]byte, 32)
	wrong, err := NewStore(dir, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.List(); err == nil {
		t.Error("a store with the wrong key read the channels")
	}
}

func TestUpdateReplacesAndKeepsTheID(t *testing.T) {
	s, _ := newStore(t)
	ch, err := s.Add(Channel{Name: "old", Provider: ProviderShoutrrr,
		Shoutrrr: ShoutrrrConfig{URL: "gotify://host/a"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	ch.Name = "new"
	ch.Enabled = false
	if err := s.Update(ch); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := s.List()
	if len(got) != 1 {
		t.Fatalf("got %d channels, want 1", len(got))
	}
	if got[0].ID != ch.ID || got[0].Name != "new" || got[0].Enabled {
		t.Errorf("channel = %+v, want the update applied under the same id", got[0])
	}
}

func TestUpdateUnknownIDFails(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Update(Channel{ID: "nope", Name: "x", Provider: ProviderShoutrrr,
		Shoutrrr: ShoutrrrConfig{URL: "gotify://h/t"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestDeleteRemoves(t *testing.T) {
	s, _ := newStore(t)
	ch, _ := s.Add(Channel{Name: "x", Provider: ProviderShoutrrr,
		Shoutrrr: ShoutrrrConfig{URL: "gotify://h/t"}})

	if err := s.Delete(ch.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := s.List()
	if len(got) != 0 {
		t.Errorf("channel survived deletion")
	}
	if err := s.Delete(ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

func TestValidationRejectsIncompleteChannels(t *testing.T) {
	s, _ := newStore(t)
	for name, ch := range map[string]Channel{
		"no name":           {Provider: ProviderShoutrrr, Shoutrrr: ShoutrrrConfig{URL: "gotify://h/t"}},
		"unknown provider":  {Name: "x", Provider: "carrier-pigeon"},
		"shoutrrr no url":   {Name: "x", Provider: ProviderShoutrrr},
		"greenapi no token": {Name: "x", Provider: ProviderGreenAPI, GreenAPI: GreenAPIConfig{InstanceID: "1", Phone: "9725"}},
		"greenapi no phone": {Name: "x", Provider: ProviderGreenAPI, GreenAPI: GreenAPIConfig{InstanceID: "1", Token: "t"}},
		"whatsapp no base":  {Name: "x", Provider: ProviderWhatsAppWeb, WhatsAppWeb: WhatsAppWebConfig{Phone: "9725"}},
		"whatsapp no phone": {Name: "x", Provider: ProviderWhatsAppWeb, WhatsAppWeb: WhatsAppWebConfig{BaseURL: "http://h"}},
	} {
		if _, err := s.Add(ch); err == nil {
			t.Errorf("%s: Add accepted an invalid channel", name)
		}
	}
}
