// Package config defines Cubit's settings and how they are loaded.
//
// Precedence is flags > environment > YAML file > built-in defaults, with the
// environment prefixed CUBIT_ (so pluxee.username is CUBIT_PLUXEE_USERNAME).
package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/t0mer/cubit/internal/session"
)

// Config is the whole of Cubit's configuration.
type Config struct {
	Server struct {
		Address string `mapstructure:"address"`
		// APIToken guards /api/v1 and /metrics. Empty leaves them open, which
		// is the documented first-run behaviour.
		APIToken string `mapstructure:"api_token"`
	} `mapstructure:"server"`

	Pluxee struct {
		// AuthBase is the CAPIR backend, which handles authentication.
		AuthBase string `mapstructure:"auth_base"`
		// APIBase is the legacy backend, which serves balances.
		APIBase      string        `mapstructure:"api_base"`
		Username     string        `mapstructure:"username"`
		Password     string        `mapstructure:"password"`
		Company      string        `mapstructure:"company"`
		RestaurantID string        `mapstructure:"restaurant_id"`
		Timeout      time.Duration `mapstructure:"timeout"`
		Language     string        `mapstructure:"language"`
		// RecaptchaToken is the escape hatch for the case where the backend
		// starts demanding a captcha on login. See docs/api-notes.md §5.
		RecaptchaToken string `mapstructure:"recaptcha_token"`
	} `mapstructure:"pluxee"`

	Voucher struct {
		ValueAgorot int64 `mapstructure:"value_agorot"`
	} `mapstructure:"voucher"`

	Auth struct {
		Autostart         bool          `mapstructure:"autostart"`
		OTPTTL            time.Duration `mapstructure:"otp_ttl"`
		OTPMaxAttempts    int           `mapstructure:"otp_max_attempts"`
		EncryptionKey     string        `mapstructure:"encryption_key"`
		EncryptionKeyFile string        `mapstructure:"encryption_key_file"`
	} `mapstructure:"auth"`

	DataDir string `mapstructure:"data_dir"`

	Log struct {
		Level  string `mapstructure:"level"`
		Format string `mapstructure:"format"`
	} `mapstructure:"log"`
}

// Default returns the built-in configuration, before any file, environment
// variable or flag is applied.
func Default() *Config {
	c := &Config{}
	c.Server.Address = ":8080"
	c.Pluxee.AuthBase = "https://api.capir.pluxee.co.il"
	c.Pluxee.APIBase = "https://api.consumers.pluxee.co.il/api/main.py"
	c.Pluxee.RestaurantID = "31999"
	c.Pluxee.Timeout = 30 * time.Second
	c.Pluxee.Language = "he"
	c.Voucher.ValueAgorot = 5000
	c.Auth.Autostart = true
	c.Auth.OTPTTL = 5 * time.Minute
	c.Auth.OTPMaxAttempts = 3
	c.DataDir = "/data"
	c.Log.Level = "info"
	c.Log.Format = "json"
	return c
}

// HasCredentials reports whether a usable Pluxee username and password are
// configured. Credentials are optional at startup because they may instead be
// supplied at runtime via POST /api/v1/auth/credentials.
func (c *Config) HasCredentials() bool {
	return strings.TrimSpace(c.Pluxee.Username) != "" && c.Pluxee.Password != ""
}

// EncryptionKey returns the 32-byte key used to encrypt the session at rest.
//
// A key file wins over an inline value, so a container can mount a secret
// without it ever appearing in the environment.
func (c *Config) EncryptionKey() ([]byte, error) {
	if path := strings.TrimSpace(c.Auth.EncryptionKeyFile); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading encryption key file: %w", err)
		}
		key, err := decodeKey(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("encryption key file %s: %w", path, err)
		}
		return key, nil
	}

	if c.Auth.EncryptionKey == "" {
		return nil, fmt.Errorf("no encryption key configured: set auth.encryption_key " +
			"(CUBIT_AUTH_ENCRYPTION_KEY) or auth.encryption_key_file")
	}
	return decodeKey(c.Auth.EncryptionKey)
}

// decodeKey turns a configured key into exactly 32 bytes. Raw, hex and base64
// are all accepted, because operators generate keys in all three shapes.
func decodeKey(v string) ([]byte, error) {
	v = strings.TrimSpace(v)
	if len(v) == session.KeyLength {
		return []byte(v), nil
	}
	if b, err := hex.DecodeString(v); err == nil && len(b) == session.KeyLength {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == session.KeyLength {
		return b, nil
	}
	return nil, fmt.Errorf("encryption key must be %d bytes: supply %d raw characters, "+
		"%d hex characters, or their base64 encoding (got %d characters)",
		session.KeyLength, session.KeyLength, session.KeyLength*2, len(v))
}

// MinAPITokenLength is the shortest API token worth having. Anything less is
// more likely a typo than a secret.
const MinAPITokenLength = 16

var validLogLevels = map[string]bool{
	"debug": true, "info": true, "warning": true, "warn": true, "error": true,
}

// Validate checks the configuration is usable. It is called at startup so that
// a bad setting fails fast rather than surfacing on the first request.
func (c *Config) Validate() error {
	// Credentials are optional here: they may be posted to
	// /api/v1/auth/credentials instead. Half a pair is always a mistake though,
	// so reject that rather than starting up in a state that cannot log in.
	hasUser := strings.TrimSpace(c.Pluxee.Username) != ""
	if hasUser && c.Pluxee.Password == "" {
		return fmt.Errorf("pluxee.password is required alongside pluxee.username (env CUBIT_PLUXEE_PASSWORD)")
	}
	if !hasUser && c.Pluxee.Password != "" {
		return fmt.Errorf("pluxee.username is required alongside pluxee.password (env CUBIT_PLUXEE_USERNAME)")
	}
	if c.Server.Address == "" {
		return fmt.Errorf("server.address is required")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if c.Voucher.ValueAgorot <= 0 {
		return fmt.Errorf("voucher.value_agorot must be positive, got %d", c.Voucher.ValueAgorot)
	}
	if c.Auth.OTPMaxAttempts <= 0 {
		return fmt.Errorf("auth.otp_max_attempts must be positive, got %d", c.Auth.OTPMaxAttempts)
	}
	if c.Auth.OTPTTL <= 0 {
		return fmt.Errorf("auth.otp_ttl must be positive, got %s", c.Auth.OTPTTL)
	}
	if !validLogLevels[strings.ToLower(c.Log.Level)] {
		return fmt.Errorf("log.level must be one of debug, info, warning, error; got %q", c.Log.Level)
	}
	if f := strings.ToLower(c.Log.Format); f != "json" && f != "text" {
		return fmt.Errorf("log.format must be json or text; got %q", c.Log.Format)
	}
	// An empty token means "open", which is allowed. A short one means the
	// operator meant to protect the API and did so ineffectively.
	if t := c.Server.APIToken; t != "" && len(t) < MinAPITokenLength {
		return fmt.Errorf("server.api_token must be at least %d characters, got %d; "+
			"leave it empty to run without authentication", MinAPITokenLength, len(t))
	}
	if _, err := c.EncryptionKey(); err != nil {
		return err
	}
	return nil
}

// String renders the configuration for logging, with every secret removed.
// Nothing that could authenticate as the user may appear here.
func (c *Config) String() string {
	return fmt.Sprintf(
		"server.address=%s server.api_token=%s data_dir=%s "+
			"pluxee.auth_base=%s pluxee.api_base=%s "+
			"pluxee.username=%s pluxee.password=%s pluxee.restaurant_id=%s "+
			"pluxee.timeout=%s pluxee.recaptcha_token=%s voucher.value_agorot=%d "+
			"auth.autostart=%t auth.otp_ttl=%s auth.otp_max_attempts=%d "+
			"auth.encryption_key=%s log.level=%s log.format=%s",
		c.Server.Address, mask(c.Server.APIToken), c.DataDir,
		c.Pluxee.AuthBase, c.Pluxee.APIBase,
		c.Pluxee.Username, mask(c.Pluxee.Password), c.Pluxee.RestaurantID,
		c.Pluxee.Timeout, mask(c.Pluxee.RecaptchaToken), c.Voucher.ValueAgorot,
		c.Auth.Autostart, c.Auth.OTPTTL, c.Auth.OTPMaxAttempts,
		mask(c.Auth.EncryptionKey), c.Log.Level, c.Log.Format)
}

// mask reports only whether a secret is set, never any part of its value.
func mask(v string) string {
	if v == "" {
		return "<unset>"
	}
	return "<set>"
}
