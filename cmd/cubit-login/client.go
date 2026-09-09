package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// cubitClient talks to a running cubit instance.
//
// Everything it sends is a secret of some kind — an OTP, a live session — so it
// never logs a request body and never includes one in an error message.
type cubitClient struct {
	base  string
	token string
	http  *http.Client
}

func newCubitClient(base, token string) *cubitClient {
	return &cubitClient{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *cubitClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encoding request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return 0, nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("X-API-Token", c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("calling %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes(), nil
}

// armRelay tells cubit an OTP is coming for a login this helper is driving.
func (c *cubitClient) armRelay(ctx context.Context, maskedTarget, method string) error {
	status, body, err := c.do(ctx, http.MethodPost, "/api/v1/auth/browser", map[string]string{
		"masked_target": maskedTarget,
		"method":        method,
	})
	if err != nil {
		return err
	}
	switch status {
	case http.StatusAccepted:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("cubit is already awaiting an otp; let it expire or restart cubit")
	case http.StatusPreconditionFailed:
		return fmt.Errorf("cubit has no api token configured, so browser handoff is disabled")
	case http.StatusUnauthorized:
		return fmt.Errorf("cubit rejected the api token")
	default:
		return fmt.Errorf("arming the relay: cubit returned %d: %s", status, firstLine(body))
	}
}

// waitForOTP polls until the user's phone posts the code, or the deadline passes.
func (c *cubitClient) waitForOTP(ctx context.Context, every time.Duration) (string, error) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		status, body, err := c.do(ctx, http.MethodGet, "/api/v1/auth/browser/otp", nil)
		if err != nil {
			return "", err
		}
		switch status {
		case http.StatusOK:
			var out struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				return "", fmt.Errorf("decoding the collected code: %w", err)
			}
			if out.Code == "" {
				return "", fmt.Errorf("cubit handed over an empty code")
			}
			return out.Code, nil
		case http.StatusNoContent:
			// nothing yet
		default:
			return "", fmt.Errorf("collecting the otp: cubit returned %d: %s", status, firstLine(body))
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// handOverSession gives cubit the cookies captured from the browser.
func (c *cubitClient) handOverSession(ctx context.Context, cookies []sessionCookie) error {
	status, body, err := c.do(ctx, http.MethodPost, "/api/v1/auth/session",
		map[string]any{"cookies": cookies})
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent:
		return nil
	case http.StatusBadRequest:
		return fmt.Errorf("cubit rejected the session: %s", firstLine(body))
	case http.StatusPreconditionFailed:
		return fmt.Errorf("cubit has no api token configured, so browser handoff is disabled")
	case http.StatusUnauthorized:
		return fmt.Errorf("cubit rejected the api token")
	default:
		return fmt.Errorf("handing over the session: cubit returned %d: %s", status, firstLine(body))
	}
}

// sessionCookie is the wire shape cubit expects.
type sessionCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
}

// firstLine keeps error text to one short line: response bodies here can carry
// state detail that is noisy on a terminal.
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// loginRequest is a pending browser-login handed over by cubit.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Company  string `json:"company"`
}

// waitForLoginRequest polls until cubit reports that a login is wanted.
//
// This is watch mode: rather than the operator running the helper, the helper
// waits and reacts to a POST to /api/v1/auth/credentials.
func (c *cubitClient) waitForLoginRequest(ctx context.Context, every time.Duration) (loginRequest, error) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		status, body, err := c.do(ctx, http.MethodGet, "/api/v1/auth/login-request", nil)
		if err != nil {
			return loginRequest{}, err
		}
		switch status {
		case http.StatusOK:
			var req loginRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return loginRequest{}, fmt.Errorf("decoding the login request: %w", err)
			}
			if req.Username == "" || req.Password == "" {
				return loginRequest{}, fmt.Errorf("cubit handed over an incomplete login request")
			}
			return req, nil
		case http.StatusNoContent:
			// nothing wanted yet
		case http.StatusPreconditionFailed:
			return loginRequest{}, fmt.Errorf("cubit has no api token configured, so browser handoff is disabled")
		case http.StatusUnauthorized:
			return loginRequest{}, fmt.Errorf("cubit rejected the api token")
		default:
			return loginRequest{}, fmt.Errorf("polling for a login request: cubit returned %d: %s",
				status, firstLine(body))
		}

		select {
		case <-ctx.Done():
			return loginRequest{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
