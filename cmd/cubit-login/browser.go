package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// The login page is an Angular SPA at consumers.pluxee.co.il. It is driven here
// through a real browser for one reason: Pluxee enforces reCAPTCHA on both the
// credential post and the OTP submission, and only a browser can mint a token.
//
// Nothing in this file is bypassing the captcha — the browser solves it exactly
// as it would for a human sitting at the keyboard.
const loginURL = "https://consumers.pluxee.co.il/login"

// Hebrew selectors, matched by visible text: the page ships no stable test ids.
const (
	xpathCookieOK   = `//button[contains(., 'אישור')]`
	xpathPasswordTb = `//*[normalize-space(text())='סיסמה קבועה']`
	xpathContinue   = `//button[contains(., 'שנמשיך')]`
	xpathSignIn     = `//button[contains(., 'כניסה')]`
	xpathMasked     = `//*[contains(text(), '***')]`
)

type browser struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// newBrowser starts Chrome. headful is worth having: if the page changes and a
// step stops matching, watching it fail is the fastest way to see why.
func newBrowser(parent context.Context, chromePath string, headful bool) *browser {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", !headful),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("lang", "he-IL"),
		chromedp.WindowSize(1280, 1000),
	)
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	return &browser{ctx: taskCtx, cancel: func() { cancelTask(); cancelAlloc() }}
}

func (b *browser) close() { b.cancel() }

// clickIfPresent clicks a best-effort element — the cookie banner and the tab
// are absent depending on prior state, and neither is worth failing over.
func clickIfPresent(ctx context.Context, xpath string, wait time.Duration) {
	c, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	_ = chromedp.Run(c, chromedp.Click(xpath, chromedp.BySearch))
}

// signIn walks the two-step login and stops at the OTP prompt, returning the
// masked destination the page reports.
func (b *browser) signIn(username, password string) (string, error) {
	if err := chromedp.Run(b.ctx,
		chromedp.Navigate(loginURL),
		chromedp.Sleep(3*time.Second),
	); err != nil {
		return "", fmt.Errorf("opening the login page: %w", err)
	}

	clickIfPresent(b.ctx, xpathCookieOK, 5*time.Second)
	clickIfPresent(b.ctx, xpathPasswordTb, 5*time.Second)

	if err := chromedp.Run(b.ctx,
		chromedp.WaitVisible(`#user`, chromedp.ByID),
		chromedp.SendKeys(`#user`, username, chromedp.ByID),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Click(xpathContinue, chromedp.BySearch),
		chromedp.WaitVisible(`#password`, chromedp.ByID),
		chromedp.Sleep(time.Second),
		chromedp.SendKeys(`#password`, password, chromedp.ByID),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Click(xpathSignIn, chromedp.BySearch),
		chromedp.Sleep(8*time.Second),
	); err != nil {
		return "", fmt.Errorf("submitting the credentials: %w", err)
	}

	// The OTP screen names the masked destination; it is display-only, so a
	// miss here is not fatal.
	var masked string
	c, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	if err := chromedp.Run(c, chromedp.Text(xpathMasked, &masked, chromedp.BySearch)); err != nil {
		masked = ""
	}
	return strings.TrimSpace(masked), nil
}

// submitOTP types the code and completes the login. The screen uses one input
// per digit, so the code is sent character by character.
func (b *browser) submitOTP(code string) error {
	var boxes []string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(`
		Array.from(document.querySelectorAll('input'))
		     .filter(e => e.offsetParent !== null &&
		                  ['text','tel','number',''].includes((e.getAttribute('type')||'')))
		     .map((e,i) => i)
	`, &boxes)); err != nil {
		return fmt.Errorf("finding the code inputs: %w", err)
	}

	// Typing into the focused field lets the SPA advance the caret itself,
	// which is what a human does and what its handlers expect.
	if err := chromedp.Run(b.ctx,
		chromedp.Evaluate(`(() => {
			const b = Array.from(document.querySelectorAll('input'))
			  .filter(e => e.offsetParent !== null);
			if (b.length) b[0].focus();
			return b.length;
		})()`, nil),
		chromedp.KeyEvent(code),
		chromedp.Sleep(1500*time.Millisecond),
	); err != nil {
		return fmt.Errorf("entering the code: %w", err)
	}

	clickIfPresent(b.ctx, xpathContinue, 5*time.Second)
	return chromedp.Run(b.ctx, chromedp.Sleep(10*time.Second))
}

// cookies reads the whole jar, including HttpOnly entries, which is the only
// reason this has to be a browser rather than an HTTP client.
func (b *browser) cookies() ([]sessionCookie, error) {
	var out []sessionCookie
	err := chromedp.Run(b.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		got, err := network.GetCookies().Do(ctx)
		if err != nil {
			return err
		}
		for _, c := range got {
			out = append(out, sessionCookie{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
				Secure: c.Secure, HTTPOnly: c.HTTPOnly, Expires: c.Expires,
			})
		}
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("reading cookies: %w", err)
	}
	return out, nil
}

// loggedIn reports whether the browser left the login page, which is the
// page's own signal that the OTP was accepted.
func (b *browser) loggedIn() bool {
	var url string
	if err := chromedp.Run(b.ctx, chromedp.Location(&url)); err != nil {
		return false
	}
	return !strings.Contains(url, "/login")
}
