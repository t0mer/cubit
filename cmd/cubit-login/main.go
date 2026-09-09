// Command cubit-login performs a browser-assisted Pluxee login and hands the
// resulting session to a running cubit instance.
//
// Why this exists: Pluxee enforces reCAPTCHA on its login endpoint, and its
// tokens are single-use and expire in about two minutes, so cubit — a static
// binary on scratch — cannot log in by itself. This helper drives a real
// browser, which solves the captcha the ordinary way, and hands over the
// session that results.
//
// The OTP still comes to the account holder's phone. Cubit catches it on
// /api/v1/auth/otp and this helper collects it, so an auto-forwarding phone app
// completes the login with nobody typing anything.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/t0mer/cubit/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cubit-login: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		cubitURL   = pflag.String("url", "http://127.0.0.1:8080", "base URL of the running cubit instance")
		token      = pflag.String("token", "", "cubit API token (or CUBIT_SERVER_API_TOKEN)")
		username   = pflag.String("username", "", "Cibus/Pluxee username (or CUBIT_PLUXEE_USERNAME)")
		otpWait    = pflag.Duration("otp-timeout", 5*time.Minute, "how long to wait for the code to arrive")
		pollEvery  = pflag.Duration("poll-interval", 2*time.Second, "how often to ask cubit for the code")
		chromePath = pflag.String("chrome", "", "path to a Chrome/Chromium binary (default: found on PATH)")
		headful    = pflag.Bool("headful", false, "show the browser window, for debugging a changed page")
		printOnly  = pflag.Bool("print", false, "print the captured session instead of sending it to cubit")
		showVer    = pflag.Bool("version", false, "print the version and exit")
	)
	pflag.Parse()

	if *showVer {
		fmt.Println(version.Version)
		return nil
	}

	// Credentials never come from a flag: flags are visible in the process table.
	if *token == "" {
		*token = os.Getenv("CUBIT_SERVER_API_TOKEN")
	}
	if *username == "" {
		*username = os.Getenv("CUBIT_PLUXEE_USERNAME")
	}
	if strings.TrimSpace(*username) == "" {
		return fmt.Errorf("a username is required (--username or CUBIT_PLUXEE_USERNAME)")
	}
	password := os.Getenv("CUBIT_PLUXEE_PASSWORD")
	if password == "" {
		var err error
		if password, err = promptPassword(); err != nil {
			return err
		}
	}
	if password == "" {
		return fmt.Errorf("a password is required")
	}
	if *token == "" && !*printOnly {
		return fmt.Errorf("cubit's api token is required (--token or CUBIT_SERVER_API_TOKEN)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := newCubitClient(*cubitURL, *token)

	fmt.Println("Starting a browser…")
	b := newBrowser(ctx, *chromePath, *headful)
	defer b.close()

	masked, err := b.signIn(*username, password)
	if err != nil {
		return err
	}
	if masked == "" {
		masked = "your phone"
	}
	fmt.Printf("Pluxee sent a one-time code to %s.\n", masked)

	if !*printOnly {
		if err := client.armRelay(ctx, masked, "phone"); err != nil {
			return err
		}
		fmt.Printf("Waiting up to %s for the code to reach cubit at %s/api/v1/auth/otp …\n",
			*otpWait, strings.TrimRight(*cubitURL, "/"))
	}

	code, err := collectCode(ctx, client, *printOnly, *otpWait, *pollEvery)
	if err != nil {
		return err
	}

	if err := b.submitOTP(code); err != nil {
		return err
	}
	if !b.loggedIn() {
		return fmt.Errorf("the login did not complete; the code may have been wrong or expired")
	}
	fmt.Println("Logged in.")

	jar, err := b.cookies()
	if err != nil {
		return err
	}
	if len(jar) == 0 {
		return fmt.Errorf("the browser produced no cookies")
	}

	if *printOnly {
		return printSession(jar)
	}
	if err := client.handOverSession(ctx, jar); err != nil {
		return err
	}
	fmt.Printf("Handed %d cookies to cubit; it is authenticated.\n", len(jar))
	return nil
}

// collectCode gets the OTP from cubit, or from the terminal when running
// standalone with --print.
func collectCode(ctx context.Context, c *cubitClient, printOnly bool,
	wait, every time.Duration) (string, error) {
	if printOnly {
		fmt.Print("Enter the code: ")
		var code string
		if _, err := fmt.Scanln(&code); err != nil {
			return "", fmt.Errorf("reading the code: %w", err)
		}
		return strings.TrimSpace(code), nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	code, err := c.waitForOTP(waitCtx, every)
	if err != nil {
		return "", fmt.Errorf("no code arrived: %w", err)
	}
	fmt.Println("Got the code from cubit.")
	return code, nil
}

func promptPassword() (string, error) {
	fmt.Print("Cibus password: ")
	raw, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	return string(raw), nil
}
