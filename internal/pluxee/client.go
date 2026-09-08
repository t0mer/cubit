// Package pluxee is the only package that knows the Pluxee/Cibus backends exist.
// Everything above it works against the API interface, so the transport can be
// faked in tests and swapped if the mobile API turns out to be a better surface.
//
// See docs/api-notes.md for how each endpoint was discovered and how confident
// we are in its shape.
package pluxee

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

const (
	// DefaultAuthBaseURL is the CAPIR backend, which handles authentication.
	DefaultAuthBaseURL = "https://api.capir.pluxee.co.il"
	// DefaultAPIBaseURL is the legacy single-endpoint backend, which handles
	// everything else including balances.
	DefaultAPIBaseURL = "https://api.consumers.pluxee.co.il/api/main.py"
	// DefaultApplicationID is the consumer SPA's public application identifier.
	// It is not a secret and not per-user.
	DefaultApplicationID = "E5D5FEF5-A05E-4C64-AEBA-BA0CECA0E402"
	// DefaultUserAgent identifies this client honestly rather than impersonating
	// a browser.
	DefaultUserAgent = "cubit/1.0 (+https://github.com/t0mer/cubit)"

	maxAttempts    = 3
	maxResponseLen = 4 << 20 // 4 MiB, plenty for any response we expect
)

// API is the surface the rest of the service depends on.
type API interface {
	Login(ctx context.Context, username, password, company string) (*LoginResult, error)
	SubmitOTP(ctx context.Context, ch *Challenge, code string) error
	Balance(ctx context.Context) (int64, error)
	Session() []*http.Cookie
	RestoreSession(cookies []*http.Cookie) error
}

// Config configures a Client. Zero values fall back to the Default* constants.
type Config struct {
	AuthBaseURL    string
	APIBaseURL     string
	ApplicationID  string
	UserAgent      string
	Language       string
	Timeout        time.Duration
	RecaptchaToken string
	Logger         *slog.Logger
}

// Client talks to the two Pluxee backends.
//
// The session is a cookie, not a bearer token (docs/api-notes.md §3), so the
// client keeps a cookie jar and the caller persists its contents.
type Client struct {
	authBase       string
	apiBase        string
	appID          string
	userAgent      string
	language       string
	recaptchaToken string

	http *http.Client
	jar  http.CookieJar
	log  *slog.Logger
}

var _ API = (*Client)(nil)

// New returns a Client. It fails only on an unusable configuration.
func New(cfg Config) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("creating cookie jar: %w", err)
	}

	c := &Client{
		authBase:       orDefault(cfg.AuthBaseURL, DefaultAuthBaseURL),
		apiBase:        orDefault(cfg.APIBaseURL, DefaultAPIBaseURL),
		appID:          orDefault(cfg.ApplicationID, DefaultApplicationID),
		userAgent:      orDefault(cfg.UserAgent, DefaultUserAgent),
		language:       orDefault(cfg.Language, "he"),
		recaptchaToken: cfg.RecaptchaToken,
		jar:            jar,
		log:            cfg.Logger,
	}
	if c.log == nil {
		c.log = slog.Default()
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	c.http = &http.Client{Jar: jar, Timeout: timeout}

	if _, err := url.Parse(c.authBase); err != nil {
		return nil, fmt.Errorf("parsing auth base url: %w", err)
	}
	if _, err := url.Parse(c.apiBase); err != nil {
		return nil, fmt.Errorf("parsing api base url: %w", err)
	}
	return c, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Session returns the cookies currently held for both backends, for persistence.
func (c *Client) Session() []*http.Cookie {
	var out []*http.Cookie
	seen := map[string]bool{}
	for _, base := range []string{c.authBase, c.apiBase} {
		u, err := url.Parse(base)
		if err != nil {
			continue
		}
		for _, ck := range c.jar.Cookies(u) {
			key := u.Host + "\x00" + ck.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			clone := *ck
			clone.Domain = u.Hostname()
			clone.Path = "/"
			out = append(out, &clone)
		}
	}
	return out
}

// RestoreSession loads previously persisted cookies back into the jar.
//
// Cookies are replayed against both backends: the session is issued by the auth
// host but must also be presented to the API host, and we cannot be sure from
// outside which parent domain the server scoped it to (docs/api-notes.md §3).
func (c *Client) RestoreSession(cookies []*http.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	for _, base := range []string{c.authBase, c.apiBase} {
		u, err := url.Parse(base)
		if err != nil {
			return fmt.Errorf("parsing base url %q: %w", base, err)
		}
		scoped := make([]*http.Cookie, 0, len(cookies))
		for _, ck := range cookies {
			clone := *ck
			clone.Domain = ""
			clone.Path = "/"
			scoped = append(scoped, &clone)
		}
		c.jar.SetCookies(u, scoped)
	}
	return nil
}

// postJSON sends body to rawURL and returns the response bytes and status.
//
// Retries are deliberately narrow: only 429 and 5xx, with exponential backoff
// and jitter, capped at maxAttempts. Authentication failures are never retried —
// this talks to a real financial account and a retry loop risks a lockout.
func (c *Client) postJSON(ctx context.Context, rawURL string, body any) ([]byte, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("encoding request: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(payload))
		if err != nil {
			return nil, 0, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("Application-Id", c.appID)
		req.Header.Set("Accept-Language", c.language)
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json, text/plain, */*")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("posting to %s: %w", redactURL(rawURL), err)
			if !c.sleepBackoff(ctx, attempt, 0) {
				return nil, 0, lastErr
			}
			continue
		}

		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseLen))
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("reading response from %s: %w", redactURL(rawURL), readErr)
			if !c.sleepBackoff(ctx, attempt, 0) {
				return nil, 0, lastErr
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%s returned http %d", redactURL(rawURL), resp.StatusCode)
			c.log.Warn("pluxee backend is unhappy, backing off",
				"url", redactURL(rawURL), "status", resp.StatusCode, "attempt", attempt)
			if !c.sleepBackoff(ctx, attempt, retryAfter(resp)) {
				return nil, resp.StatusCode, lastErr
			}
			continue
		}

		return raw, resp.StatusCode, nil
	}
	return nil, 0, lastErr
}

// sleepBackoff waits before the next attempt and reports whether another attempt
// should be made. It honours Retry-After when the server sent one.
func (c *Client) sleepBackoff(ctx context.Context, attempt int, after time.Duration) bool {
	if attempt >= maxAttempts {
		return false
	}
	delay := time.Duration(1<<uint(attempt-1)) * time.Second
	if after > 0 {
		delay = after
	}
	// Jitter so concurrent clients do not synchronise their retries.
	delay += time.Duration(rand.Int64N(int64(500 * time.Millisecond)))

	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err != nil || secs <= 0 {
		return 0
	}
	if secs > 300 {
		secs = 300
	}
	return time.Duration(secs) * time.Second
}

// redactURL strips any query string, which is where a token would hide.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	u.RawQuery = ""
	u.User = nil
	return u.String()
}

// decodeAuth parses a CAPIR response, preferring the body's status field over
// the HTTP status line.
func decodeAuth(raw []byte, httpStatus int) (*authResponse, error) {
	var out authResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding auth response (http %d): %w", httpStatus, err)
	}
	if out.Status == 0 {
		out.Status = httpStatus
	}
	return &out, nil
}

// errUnexpected builds an error for a status we have no specific handling for,
// without echoing the response body (which can contain personal data).
func errUnexpected(r *authResponse) error {
	msg := r.Error.Message
	if msg == "" {
		msg = r.Message
	}
	if msg == "" {
		msg = "no message"
	}
	return fmt.Errorf("pluxee: unexpected auth status %d: %s", r.Status, msg)
}

var errEmptyResponse = errors.New("pluxee: empty response body")
