package pluxee

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testClient wires a Client to a pair of test servers standing in for the two
// real backends.
func testClient(t *testing.T, authHandler, apiHandler http.HandlerFunc) *Client {
	t.Helper()
	auth := httptest.NewServer(authHandler)
	api := httptest.NewServer(apiHandler)
	t.Cleanup(auth.Close)
	t.Cleanup(api.Close)

	c, err := New(Config{
		AuthBaseURL: auth.URL,
		APIBaseURL:  api.URL,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decoding body %q: %v", raw, err)
	}
	return m
}

func TestRequestsCarryRequiredHeaders(t *testing.T) {
	var got http.Header
	c := testClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Clone()
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"status":401}`)
		}, nil)

	_, _ = c.Login(context.Background(), "user", "pass", "")

	if v := got.Get("Application-Id"); v != DefaultApplicationID {
		t.Errorf("Application-Id = %q, want %q", v, DefaultApplicationID)
	}
	if v := got.Get("Content-Type"); !strings.HasPrefix(v, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", v)
	}
	if got.Get("Accept-Language") == "" {
		t.Error("Accept-Language header missing")
	}
	if got.Get("User-Agent") == "" {
		t.Error("User-Agent header missing")
	}
}

func TestLoginOTPRequiredReturnsChallenge(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/authToken" {
			t.Errorf("login posted to %q, want /auth/authToken", r.URL.Path)
		}
		body := decodeBody(t, r)
		if body["username"] != "alice" || body["password"] != "secret" {
			t.Errorf("unexpected login body: %v", body)
		}
		w.WriteHeader(210)
		io.WriteString(w, `{"status":210,"data":{"maskedInput":"05*-***1234",
			"method":"sms","userInput1":"alice","userInput2":"acme"}}`)
	}, nil)

	res, err := c.Login(context.Background(), "alice", "secret", "acme")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Authenticated {
		t.Error("Authenticated = true, want false when an OTP is required")
	}
	if res.Challenge == nil {
		t.Fatal("Challenge is nil, want a challenge")
	}
	if res.Challenge.MaskedInput != "05*-***1234" {
		t.Errorf("MaskedInput = %q", res.Challenge.MaskedInput)
	}
	if res.Challenge.Method != "sms" {
		t.Errorf("Method = %q", res.Challenge.Method)
	}
	if res.Challenge.UserInput1 != "alice" {
		t.Errorf("UserInput1 = %q, want it echoed from the response", res.Challenge.UserInput1)
	}
}

// The status can arrive in the JSON body rather than the HTTP status line.
func TestLoginReadsStatusFromBodyWhenHTTPIs200(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"status":210,"data":{"maskedInput":"05*-***9999",`+
			`"userInput1":"6aa15a3ce420d57d9b990ff4","method":"phone"}}`)
	}, nil)

	res, err := c.Login(context.Background(), "alice", "secret", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Challenge == nil {
		t.Fatal("Challenge is nil; body status 210 was not honoured")
	}
}

func TestLoginWithoutOTPIsAuthenticated(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "token", Value: "abc", Path: "/"})
		io.WriteString(w, `{"status":200,"data":{}}`)
	}, nil)

	res, err := c.Login(context.Background(), "alice", "secret", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !res.Authenticated {
		t.Error("Authenticated = false, want true")
	}
	if res.Challenge != nil {
		t.Error("Challenge non-nil, want nil when already authenticated")
	}
}

func TestLoginBadCredentials(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"status":401,"error":{"code":"Unauthorized","message":"Unauthorized"}}`)
	}, nil)
	// Without a token configured a 401 means "captcha", not "wrong password" —
	// see TestLoginWithoutACaptchaTokenReportsCaptchaNotBadCredentials.
	c.recaptchaToken = "a-browser-minted-token"

	_, err := c.Login(context.Background(), "alice", "wrong", "")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginCaptchaRequiredIsDistinguishable(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"status":400,"error":{"code":"Bad Request",
			"message":"Invalid reCAPTCHA. Please try again"}}`)
	}, nil)

	_, err := c.Login(context.Background(), "alice", "secret", "")
	if !errors.Is(err, ErrCaptchaRequired) {
		t.Fatalf("Login error = %v, want ErrCaptchaRequired", err)
	}
}

func TestLoginDoesNotRetryOnAuthFailure(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"status":401}`)
	}, nil)

	_, _ = c.Login(context.Background(), "alice", "wrong", "")

	if calls != 1 {
		t.Fatalf("auth endpoint called %d times, want exactly 1 — never retry a credential failure", calls)
	}
}

func TestSubmitOTPPostsPinAndEchoedInputs(t *testing.T) {
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		http.SetCookie(w, &http.Cookie{Name: "token", Value: "abc", Path: "/"})
		io.WriteString(w, `{"status":200,"data":{}}`)
	}, nil)

	ch := &Challenge{UserInput1: "alice", UserInput2: "acme"}
	if err := c.SubmitOTP(context.Background(), ch, "123456"); err != nil {
		t.Fatalf("SubmitOTP: %v", err)
	}
	if body["otpPin"] != "123456" {
		t.Errorf("otpPin = %v, want 123456", body["otpPin"])
	}
	if body["userInput1"] != "alice" || body["userInput2"] != "acme" {
		t.Errorf("challenge inputs not echoed: %v", body)
	}
	if _, ok := body["username"]; ok {
		t.Error("OTP submission must not resend the password login fields")
	}
}

func TestSubmitOTPRejectedCode(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"status":401}`)
	}, nil)

	err := c.SubmitOTP(context.Background(), &Challenge{}, "000000")
	if !errors.Is(err, ErrOTPRejected) {
		t.Fatalf("SubmitOTP error = %v, want ErrOTPRejected", err)
	}
}

func TestBalanceSumsCurrBudget(t *testing.T) {
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if body := decodeBody(t, r); body["type"] != "prx_get_budgets" {
			t.Errorf("request type = %v, want prx_get_budgets", body["type"])
		}
		io.WriteString(w, `{"code":0,"data":[
			{"CurrBudget":"200.00","CreationDate":"2026-09-01"},
			{"CurrBudget":"73.50","CreationDate":"2026-09-02"}]}`)
	})

	got, err := c.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if got != 27350 {
		t.Errorf("Balance = %d agorot, want 27350", got)
	}
}

func TestBalanceAcceptsNumericCurrBudget(t *testing.T) {
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":0,"data":[{"CurrBudget":1.15}]}`)
	})

	got, err := c.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if got != 115 {
		t.Errorf("Balance = %d agorot, want 115 — numeric budgets must not go through float64", got)
	}
}

func TestBalanceEmptyIsZero(t *testing.T) {
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":0,"data":[]}`)
	})

	got, err := c.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if got != 0 {
		t.Errorf("Balance = %d, want 0", got)
	}
}

// The legacy backend answers HTTP 200 even for failures; the real status is the
// body's code field.
func TestBalanceTreatsAuthorizationCodeAsSessionExpiry(t *testing.T) {
	for _, code := range []int{172, 177, 179} {
		c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"code":`+strconv.Itoa(code)+`,"msg":"Can't find cookie token","http_code":401}`)
		})

		_, err := c.Balance(context.Background())
		if !errors.Is(err, ErrSessionExpired) {
			t.Errorf("code %d: Balance error = %v, want ErrSessionExpired", code, err)
		}
	}
}

func TestBalanceNonZeroCodeIsAnError(t *testing.T) {
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":42,"msg":"something broke","http_code":500}`)
	})

	_, err := c.Balance(context.Background())
	if err == nil {
		t.Fatal("Balance returned nil error for a non-zero response code on HTTP 200")
	}
	if errors.Is(err, ErrSessionExpired) {
		t.Fatal("code 42 must not be treated as session expiry")
	}
	if !strings.Contains(err.Error(), "something broke") {
		t.Errorf("error %v does not surface the API message", err)
	}
}

func TestSessionCookiesRoundTrip(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "token", Value: "sekrit", Path: "/"})
		io.WriteString(w, `{"status":200,"data":{}}`)
	}, nil)

	if _, err := c.Login(context.Background(), "alice", "secret", ""); err != nil {
		t.Fatalf("Login: %v", err)
	}

	saved := c.Session()
	if len(saved) == 0 {
		t.Fatal("Session() returned no cookies after a successful login")
	}

	restored := testClient(t, nil, nil)
	if err := restored.RestoreSession(saved); err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	if len(restored.Session()) != len(saved) {
		t.Errorf("restored %d cookies, want %d", len(restored.Session()), len(saved))
	}
}
