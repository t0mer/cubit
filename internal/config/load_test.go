package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadReadsYAML(t *testing.T) {
	path := writeYAML(t, `
server:
  address: ":9999"
voucher:
  value_agorot: 10000
pluxee:
  username: from-yaml
`)
	c, err := Load(path, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Server.Address != ":9999" {
		t.Errorf("address = %q, want :9999", c.Server.Address)
	}
	if c.Voucher.ValueAgorot != 10000 {
		t.Errorf("voucher value = %d, want 10000", c.Voucher.ValueAgorot)
	}
	if c.Pluxee.Username != "from-yaml" {
		t.Errorf("username = %q", c.Pluxee.Username)
	}
}

func TestLoadKeepsDefaultsForUnsetKeys(t *testing.T) {
	path := writeYAML(t, "server:\n  address: \":9999\"\n")
	c, err := Load(path, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pluxee.RestaurantID != "31999" {
		t.Errorf("restaurant id = %q, want the default 31999", c.Pluxee.RestaurantID)
	}
	if c.Voucher.ValueAgorot != 5000 {
		t.Errorf("voucher value = %d, want the default 5000", c.Voucher.ValueAgorot)
	}
}

func TestEnvironmentOverridesYAML(t *testing.T) {
	path := writeYAML(t, "pluxee:\n  username: from-yaml\n")
	t.Setenv("CUBIT_PLUXEE_USERNAME", "from-env")

	c, err := Load(path, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pluxee.Username != "from-env" {
		t.Errorf("username = %q, want the environment to win over the file", c.Pluxee.Username)
	}
}

func TestFlagOverridesEnvironment(t *testing.T) {
	path := writeYAML(t, "server:\n  address: \":1111\"\n")
	t.Setenv("CUBIT_SERVER_ADDRESS", ":2222")

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	Register(fs)
	if err := fs.Parse([]string{"--address", ":3333"}); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path, fs)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Address != ":3333" {
		t.Errorf("address = %q, want the flag to win over the environment", c.Server.Address)
	}
}

func TestUnsetFlagDoesNotClobberEnvironment(t *testing.T) {
	t.Setenv("CUBIT_SERVER_ADDRESS", ":2222")

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	Register(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}

	c, err := Load("", fs)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Address != ":2222" {
		t.Errorf("address = %q; an unset flag must not override the environment", c.Server.Address)
	}
}

func TestMissingConfigFileIsNotAnError(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.yaml"),
		pflag.NewFlagSet("test", pflag.ContinueOnError))
	if err != nil {
		t.Fatalf("Load with an absent config file: %v", err)
	}
	if c.Server.Address != ":8080" {
		t.Errorf("address = %q, want the default", c.Server.Address)
	}
}

func TestMalformedConfigFileIsAnError(t *testing.T) {
	path := writeYAML(t, "server: [this is not a mapping\n")
	if _, err := Load(path, pflag.NewFlagSet("test", pflag.ContinueOnError)); err == nil {
		t.Fatal("Load accepted a malformed YAML file")
	}
}

func TestEnvironmentSuppliesDurationsAndBooleans(t *testing.T) {
	t.Setenv("CUBIT_AUTH_OTP_TTL", "90s")
	t.Setenv("CUBIT_AUTH_AUTOSTART", "false")

	c, err := Load("", pflag.NewFlagSet("test", pflag.ContinueOnError))
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.OTPTTL.String() != "1m30s" {
		t.Errorf("otp ttl = %v, want 1m30s", c.Auth.OTPTTL)
	}
	if c.Auth.Autostart {
		t.Error("autostart = true, want false from the environment")
	}
}
