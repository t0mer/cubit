package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// EnvPrefix is the prefix for every environment override: pluxee.username is
// read from CUBIT_PLUXEE_USERNAME.
const EnvPrefix = "CUBIT"

// DefaultConfigPath is where the container looks for its YAML file.
const DefaultConfigPath = "/config/config.yaml"

// flagToKey maps a command-line flag onto the configuration key it overrides.
var flagToKey = map[string]string{
	"address":       "server.address",
	"data-dir":      "data_dir",
	"log-level":     "log.level",
	"log-format":    "log.format",
	"restaurant-id": "pluxee.restaurant_id",
	"voucher-value": "voucher.value_agorot",
	"autostart":     "auth.autostart",
	"otp-ttl":       "auth.otp_ttl",
}

// Register declares the command-line flags Cubit understands.
//
// Credentials are deliberately absent: a password passed as a flag is visible in
// the process table, so they come from the environment or the config file only.
func Register(fs *pflag.FlagSet) {
	d := Default()
	fs.String("address", d.Server.Address, "address to listen on")
	fs.String("config", DefaultConfigPath, "path to the YAML configuration file")
	fs.String("data-dir", d.DataDir, "directory for the encrypted session file")
	fs.String("log-level", d.Log.Level, "log level: debug, info, warning or error")
	fs.String("log-format", d.Log.Format, "log format: json or text")
	fs.String("restaurant-id", d.Pluxee.RestaurantID, "restaurant to price vouchers against")
	fs.Int64("voucher-value", d.Voucher.ValueAgorot, "voucher denomination in agorot")
	fs.Bool("autostart", d.Auth.Autostart, "begin login on boot when no valid session is held")
	fs.Duration("otp-ttl", d.Auth.OTPTTL, "how long an OTP challenge stays valid")
	fs.Bool("version", false, "print the version and exit")
}

// Load assembles the configuration from defaults, the YAML file, the
// environment and the flags, in that order of increasing precedence.
//
// An absent config file is fine — the container may be configured entirely by
// environment variables. A malformed one is not.
func Load(path string, fs *pflag.FlagSet) (*Config, error) {
	v := viper.New()

	// Defaults first, so every key exists and Viper can see it for env binding.
	setDefaults(v, Default())

	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) && !os.IsNotExist(err) {
				return nil, fmt.Errorf("reading config file %s: %w", path, err)
			}
		}
	}

	// Flags last, and only those the user actually set: binding an unset flag
	// would let its default silently override an environment variable.
	if fs != nil {
		var bindErr error
		fs.Visit(func(f *pflag.Flag) {
			if key, ok := flagToKey[f.Name]; ok {
				if err := v.BindPFlag(key, f); err != nil {
					bindErr = fmt.Errorf("binding flag --%s: %w", f.Name, err)
				}
			}
		})
		if bindErr != nil {
			return nil, bindErr
		}
	}

	cfg := Default()
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("decoding configuration: %w", err)
	}
	return cfg, nil
}

func setDefaults(v *viper.Viper, d *Config) {
	v.SetDefault("server.address", d.Server.Address)
	v.SetDefault("server.api_token", d.Server.APIToken)
	v.SetDefault("pluxee.auth_base", d.Pluxee.AuthBase)
	v.SetDefault("pluxee.api_base", d.Pluxee.APIBase)
	v.SetDefault("pluxee.username", d.Pluxee.Username)
	v.SetDefault("pluxee.password", d.Pluxee.Password)
	v.SetDefault("pluxee.company", d.Pluxee.Company)
	v.SetDefault("pluxee.restaurant_id", d.Pluxee.RestaurantID)
	v.SetDefault("pluxee.timeout", d.Pluxee.Timeout)
	v.SetDefault("pluxee.language", d.Pluxee.Language)
	v.SetDefault("pluxee.recaptcha_token", d.Pluxee.RecaptchaToken)
	v.SetDefault("voucher.value_agorot", d.Voucher.ValueAgorot)
	v.SetDefault("auth.autostart", d.Auth.Autostart)
	v.SetDefault("auth.otp_ttl", d.Auth.OTPTTL)
	v.SetDefault("auth.otp_max_attempts", d.Auth.OTPMaxAttempts)
	v.SetDefault("auth.encryption_key", d.Auth.EncryptionKey)
	v.SetDefault("auth.encryption_key_file", d.Auth.EncryptionKeyFile)
	v.SetDefault("data_dir", d.DataDir)
	v.SetDefault("log.level", d.Log.Level)
	v.SetDefault("log.format", d.Log.Format)
}
