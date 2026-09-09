package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// watchLoop keeps the helper running beside cubit, logging in whenever
// credentials are posted to /api/v1/auth/credentials.
//
// This is what makes a single POST behave like a login: the operator posts
// credentials, cubit records that a login is wanted, and this loop drives a
// real browser through it — the only way past Pluxee's reCAPTCHA.
//
// A failed attempt is logged and the loop carries on waiting. It never retries
// by itself: a repeated automatic login against a financial account is exactly
// what the build contract forbids.
func watchLoop(cubitURL, token, chromePath string, headful, noSandbox bool,
	otpWait, pollEvery time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := newCubitClient(cubitURL, token)
	fmt.Printf("Watching %s — post credentials to /api/v1/auth/credentials to log in.\n", cubitURL)

	for {
		req, err := client.waitForLoginRequest(ctx, pollEvery)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Println("Stopped.")
				return nil
			}
			return err
		}

		fmt.Println("A login was requested; starting a browser…")
		if err := runLogin(ctx, client, req, chromePath, headful, noSandbox, otpWait, pollEvery); err != nil {
			// Report and keep waiting. The next POST is the retry, and it is the
			// operator's decision, not ours.
			fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
			continue
		}
		fmt.Println("Logged in; cubit is authenticated.")
	}
}

// runLogin performs one browser-assisted login end to end.
func runLogin(ctx context.Context, client *cubitClient, req loginRequest,
	chromePath string, headful, noSandbox bool, otpWait, pollEvery time.Duration) error {
	b := newBrowser(ctx, chromePath, headful, noSandbox)
	defer b.close()

	masked, err := b.signIn(req.Username, req.Password)
	if err != nil {
		return err
	}
	if masked == "" {
		masked = "the account holder's phone"
	}
	fmt.Printf("Pluxee sent a one-time code to %s.\n", masked)

	if err := client.armRelay(ctx, masked, "phone"); err != nil {
		return err
	}

	waitCtx, cancel := context.WithTimeout(ctx, otpWait)
	defer cancel()
	code, err := client.waitForOTP(waitCtx, pollEvery)
	if err != nil {
		return fmt.Errorf("no code arrived: %w", err)
	}

	if err := b.submitOTP(code); err != nil {
		return err
	}
	if !b.loggedIn() {
		return fmt.Errorf("the login did not complete; the code may have been wrong or expired")
	}

	jar, err := b.cookies()
	if err != nil {
		return err
	}
	if len(jar) == 0 {
		return fmt.Errorf("the browser produced no cookies")
	}
	return client.handOverSession(ctx, jar)
}
