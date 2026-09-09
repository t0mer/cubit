// Command cubit reads a Cibus/Pluxee meal-card balance and reports how many
// fixed-denomination vouchers it covers.
//
// Logging in needs a human: Pluxee sends a one-time code out of band, so the
// service parks in AWAITING_OTP and waits for the code to be posted to
// /api/v1/auth/otp. A successful session is encrypted to disk, so a restart
// normally does not need a new code.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/t0mer/cubit/internal/api"
	"github.com/t0mer/cubit/internal/config"
	"github.com/t0mer/cubit/internal/metrics"
	"github.com/t0mer/cubit/internal/notify"
	"github.com/t0mer/cubit/internal/pluxee"
	"github.com/t0mer/cubit/internal/session"
	"github.com/t0mer/cubit/internal/version"
	"github.com/t0mer/cubit/internal/voucher"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "cubit:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "cubit",
		Short:         "Report the Cibus balance and how many vouchers it buys",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showVersion, _ := cmd.Flags().GetBool("version"); showVersion {
				fmt.Println(version.Version)
				return nil
			}
			configPath, _ := cmd.Flags().GetString("config")
			return run(cmd.Context(), configPath, cmd.Flags())
		},
	}
	config.Register(cmd.Flags())
	return cmd
}

func run(ctx context.Context, configPath string, flags *pflag.FlagSet) error {
	cfg, err := config.Load(configPath, flags)
	if err != nil {
		return err
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration is not usable: %w", err)
	}
	logger.Info("starting cubit", "version", version.Version)
	if cfg.Server.APIToken == "" {
		logger.Warn("no api token configured: /api/v1 and /metrics are open to anyone " +
			"who can reach this port, which exposes the balance and can trigger an OTP. " +
			"Set CUBIT_SERVER_API_TOKEN to require one.")
	}
	logger.Debug("configuration", "settings", cfg.String())

	key, err := cfg.EncryptionKey()
	if err != nil {
		return err
	}
	store, err := session.NewStore(cfg.DataDir, key)
	if err != nil {
		return err
	}

	client, err := pluxee.New(pluxee.Config{
		AuthBaseURL:    cfg.Pluxee.AuthBase,
		APIBaseURL:     cfg.Pluxee.APIBase,
		Language:       cfg.Pluxee.Language,
		Timeout:        cfg.Pluxee.Timeout,
		RecaptchaToken: cfg.Pluxee.RecaptchaToken,
		Logger:         logger,
	})
	if err != nil {
		return err
	}

	calc, err := voucher.New(cfg.Voucher.ValueAgorot)
	if err != nil {
		return err
	}

	sessions, err := session.NewManager(session.Options{
		Client:         client,
		Store:          store,
		Username:       cfg.Pluxee.Username,
		Password:       cfg.Pluxee.Password,
		Company:        cfg.Pluxee.Company,
		OTPTTL:         cfg.Auth.OTPTTL,
		OTPMaxAttempts: cfg.Auth.OTPMaxAttempts,
		Logger:         logger,
	})
	if err != nil {
		return err
	}

	channels, err := notify.NewStore(cfg.DataDir, key)
	if err != nil {
		return err
	}

	handler, err := api.New(api.Options{
		Sessions:     sessions,
		Client:       client,
		Calculator:   calc,
		RestaurantID: cfg.Pluxee.RestaurantID,
		Metrics:      metrics.New(),
		Logger:       logger,
		Version:      version.Version,
		APIToken:     cfg.Server.APIToken,
		Channels:     channels,
		Notifier:     notify.NewNotifier(channels, nil, logger),
		// Stage 1's headline requirement: print the result.
		OnReport: func(r api.BalanceReport) { fmt.Print(r.Render()) },
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	bootstrap(ctx, sessions, handler, cfg, logger)

	srv := &http.Server{
		Addr:              cfg.Server.Address,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "address", cfg.Server.Address)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

// bootstrap restores a persisted session if one is usable, and otherwise starts
// a login so the user receives their code without having to poke the service.
//
// Nothing here is fatal: the service must come up and serve /healthz and
// /api/v1/auth/login even when Pluxee is unreachable.
func bootstrap(ctx context.Context, sessions *session.Manager, handler *api.Handler,
	cfg *config.Config, logger *slog.Logger) {

	bootCtx, cancel := context.WithTimeout(ctx, cfg.Pluxee.Timeout+5*time.Second)
	defer cancel()

	switch err := sessions.Restore(bootCtx); {
	case err == nil:
		logger.Info("restored a session from disk; no otp needed")
		if _, err := handler.Check(bootCtx); err != nil {
			logger.Warn("could not read the balance at startup", "error", err)
		}
		return
	case errors.Is(err, session.ErrNoSession):
		logger.Info("no usable session on disk")
	default:
		logger.Warn("could not restore the persisted session", "error", err)
	}

	if !cfg.HasCredentials() {
		logger.Info("no pluxee credentials configured; " +
			"POST them to /api/v1/auth/credentials, then POST /api/v1/auth/login")
		return
	}

	if !cfg.Auth.Autostart {
		logger.Info("autostart is off; POST /api/v1/auth/login to begin")
		return
	}

	ch, err := sessions.Login(bootCtx)
	switch {
	case err != nil:
		logger.Error("could not start the login; POST /api/v1/auth/login to retry", "error", err)
	case ch == nil:
		logger.Info("authenticated without an otp")
		if _, err := handler.Check(bootCtx); err != nil {
			logger.Warn("could not read the balance at startup", "error", err)
		}
	default:
		fmt.Printf("An OTP was sent by %s to %s.\n"+
			"Submit it with: curl -X POST %s/api/v1/auth/otp "+
			"-H 'Content-Type: application/json' -d '{\"code\":\"123456\"}'\n",
			ch.Method, ch.MaskedInput, displayAddress(cfg.Server.Address))
	}
}

// displayAddress turns a listen address into something a user can paste.
func displayAddress(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Log.Level)}
	if strings.EqualFold(cfg.Log.Format, "text") {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warning", "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
