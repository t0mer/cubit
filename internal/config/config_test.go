package config

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeKeyAcceptsRaw32Bytes(t *testing.T) {
	raw := strings.Repeat("k", 32)
	got, err := decodeKey(raw)
	if err != nil {
		t.Fatalf("decodeKey: %v", err)
	}
	if string(got) != raw {
		t.Errorf("decodeKey = %q, want the raw bytes back", got)
	}
}

func TestDecodeKeyAcceptsHex(t *testing.T) {
	want := []byte(strings.Repeat("a", 32))
	got, err := decodeKey(hex.EncodeToString(want))
	if err != nil {
		t.Fatalf("decodeKey: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decodeKey did not decode the hex key")
	}
}

func TestDecodeKeyAcceptsBase64(t *testing.T) {
	want := []byte(strings.Repeat("b", 32))
	got, err := decodeKey(base64.StdEncoding.EncodeToString(want))
	if err != nil {
		t.Fatalf("decodeKey: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("decodeKey did not decode the base64 key")
	}
}

func TestDecodeKeyRejectsWrongLength(t *testing.T) {
	for _, in := range []string{"", "short", strings.Repeat("k", 31), strings.Repeat("k", 33)} {
		if _, err := decodeKey(in); err == nil {
			t.Errorf("decodeKey(%d chars) accepted a key that is not 32 bytes", len(in))
		}
	}
}

func TestEncryptionKeyFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	// Trailing newline is what an operator's `echo ... > key` produces.
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Config{}
	c.Auth.EncryptionKeyFile = path

	key, err := c.EncryptionKey()
	if err != nil {
		t.Fatalf("EncryptionKey: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32 (the trailing newline should be trimmed)", len(key))
	}
}

func TestEncryptionKeyFilePreferredOverInlineValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte(strings.Repeat("f", 32)), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Config{}
	c.Auth.EncryptionKey = strings.Repeat("i", 32)
	c.Auth.EncryptionKeyFile = path

	key, err := c.EncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != strings.Repeat("f", 32) {
		t.Error("the key file should win over the inline value")
	}
}

func TestEncryptionKeyMissingIsAnError(t *testing.T) {
	c := &Config{}
	if _, err := c.EncryptionKey(); err == nil {
		t.Fatal("EncryptionKey with nothing configured returned no error")
	}
}

func validConfig() *Config {
	c := Default()
	c.Pluxee.Username = "alice"
	c.Pluxee.Password = "secret"
	c.Auth.EncryptionKey = strings.Repeat("k", 32)
	return c
}

func TestValidateAcceptsAGoodConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRequiresCredentials(t *testing.T) {
	c := validConfig()
	c.Pluxee.Username = ""
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate accepted a config with no username")
	}
	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error %v should name the missing setting", err)
	}

	c = validConfig()
	c.Pluxee.Password = ""
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted a config with no password")
	}
}

func TestValidateRequiresAUsableEncryptionKey(t *testing.T) {
	c := validConfig()
	c.Auth.EncryptionKey = "too-short"
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted a short encryption key")
	}
}

func TestValidateRejectsNonPositiveVoucherValue(t *testing.T) {
	for _, v := range []int64{0, -1} {
		c := validConfig()
		c.Voucher.ValueAgorot = v
		if err := c.Validate(); err == nil {
			t.Errorf("Validate accepted a voucher value of %d", v)
		}
	}
}

func TestValidateRejectsBadLogLevel(t *testing.T) {
	c := validConfig()
	c.Log.Level = "chatty"
	if err := c.Validate(); err == nil {
		t.Fatal("Validate accepted an unknown log level")
	}
}

func TestDefaultsMatchTheContract(t *testing.T) {
	c := Default()
	if c.Server.Address != ":8080" {
		t.Errorf("server address = %q, want :8080", c.Server.Address)
	}
	if c.Voucher.ValueAgorot != 5000 {
		t.Errorf("voucher value = %d, want 5000 agorot", c.Voucher.ValueAgorot)
	}
	if c.Pluxee.RestaurantID != "31999" {
		t.Errorf("restaurant id = %q, want 31999", c.Pluxee.RestaurantID)
	}
	if !c.Auth.Autostart {
		t.Error("autostart should default to true")
	}
	if c.Auth.OTPMaxAttempts != 3 {
		t.Errorf("otp max attempts = %d, want 3", c.Auth.OTPMaxAttempts)
	}
	if c.Auth.OTPTTL.String() != "5m0s" {
		t.Errorf("otp ttl = %v, want 5m", c.Auth.OTPTTL)
	}
	if c.DataDir != "/data" {
		t.Errorf("data dir = %q, want /data", c.DataDir)
	}
}

// Credentials must never end up in a log line, so Config must not print them.
func TestConfigStringRedactsSecrets(t *testing.T) {
	c := validConfig()
	c.Pluxee.Password = "hunter2"
	c.Auth.EncryptionKey = strings.Repeat("s", 32)
	c.Pluxee.RecaptchaToken = "03AGdBq26..."

	printed := c.String()
	for _, secret := range []string{"hunter2", strings.Repeat("s", 32), "03AGdBq26..."} {
		if strings.Contains(printed, secret) {
			t.Errorf("Config.String() leaked %q", secret)
		}
	}
	if !strings.Contains(printed, "alice") {
		t.Error("Config.String() should still show non-secret settings like the username")
	}
}
