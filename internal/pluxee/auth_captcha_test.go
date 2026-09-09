package pluxee

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// testClientWithToken is testClient plus a configured reCAPTCHA token.
func testClientWithToken(t *testing.T, token string, authHandler http.HandlerFunc) *Client {
	t.Helper()
	c := testClient(t, authHandler, func(w http.ResponseWriter, _ *http.Request) {})
	c.recaptchaToken = token
	_ = time.Second
	return c
}

// A 401 from authToken is ambiguous. Verified live on 2026-09-09: the real
// backend answers 401 to a correct username and password when no reCAPTCHA
// token is sent, and 210 to the very same credentials when one is. So a 401
// with no token configured must not be reported as bad credentials — that
// sends the user off chasing a password that is perfectly fine.
func TestLoginWithoutACaptchaTokenReportsCaptchaNotBadCredentials(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"status":401,"error":{"code":"Unauthorized","message":"Unauthorized"}}`)
	}, func(w http.ResponseWriter, _ *http.Request) {})

	_, err := c.Login(context.Background(), "alice", "secret", "")
	if !errors.Is(err, ErrCaptchaRequired) {
		t.Fatalf("Login error = %v, want ErrCaptchaRequired", err)
	}
}

// With a token actually supplied, a 401 means what it says.
func TestLoginWithACaptchaTokenReportsBadCredentials(t *testing.T) {
	c := testClientWithToken(t, "a-browser-minted-token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"status":401,"error":{"code":"Unauthorized","message":"Unauthorized"}}`)
	})

	_, err := c.Login(context.Background(), "alice", "secret", "")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
}

// userInput1 is an opaque server-issued handle (observed: a 24-char hex
// string), not the username. Falling back to the username would build a
// malformed OTP submission, so an absent handle must be an error.
func TestLoginRejectsAChallengeWithNoUserInputHandle(t *testing.T) {
	c := testClientWithToken(t, "token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":210,"data":{"maskedInput":"***-***1865","method":"phone"}}`)
	})

	_, err := c.Login(context.Background(), "alice", "secret", "")
	if !errors.Is(err, ErrIncompleteChallenge) {
		t.Fatalf("Login error = %v, want ErrIncompleteChallenge", err)
	}
}

// The shape verified live on 2026-09-09.
func TestLoginCarriesTheOpaqueHandleFromTheChallenge(t *testing.T) {
	c := testClientWithToken(t, "token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":210,"message":"Request successful","error":{},`+
			`"data":{"maskedInput":"***-***1865","userInput1":"6aa15a3ce420d57d9b990ff4","method":"phone"}}`)
	})

	res, err := c.Login(context.Background(), "alice", "secret", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Challenge == nil {
		t.Fatal("no challenge returned")
	}
	if got := res.Challenge.UserInput1; got != "6aa15a3ce420d57d9b990ff4" {
		t.Errorf("UserInput1 = %q, want the opaque handle, not the username", got)
	}
	if got := res.Challenge.Method; got != "phone" {
		t.Errorf("Method = %q, want %q", got, "phone")
	}
}
