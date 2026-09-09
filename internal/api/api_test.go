package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/cubit/internal/metrics"
	"github.com/t0mer/cubit/internal/notify"
	"github.com/t0mer/cubit/internal/pluxee"
	"github.com/t0mer/cubit/internal/session"
	"github.com/t0mer/cubit/internal/voucher"
)

type fakeAPI struct {
	mu sync.Mutex

	loginResult *pluxee.LoginResult
	loginErr    error
	lastUser    string
	otpErr      error
	balance     int64
	balanceErr  error
	cookies     []*http.Cookie
	logoutCalls int
	logoutErr   error
}

func (f *fakeAPI) Login(_ context.Context, user, _, _ string) (*pluxee.LoginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastUser = user
	return f.loginResult, f.loginErr
}

func (f *fakeAPI) SubmitOTP(context.Context, *pluxee.Challenge, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.otpErr == nil {
		f.cookies = []*http.Cookie{{Name: "token", Value: "ok"}}
	}
	return f.otpErr
}

func (f *fakeAPI) Balance(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balance, f.balanceErr
}

func (f *fakeAPI) Session() []*http.Cookie { return f.cookies }

func (f *fakeAPI) RestoreSession(c []*http.Cookie) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cookies = c
	return nil
}

func challenge() *pluxee.LoginResult {
	return &pluxee.LoginResult{Challenge: &pluxee.Challenge{
		UserInput1: "alice", MaskedInput: "05*-***1234", Method: "sms",
	}}
}

func newServer(t *testing.T, api *fakeAPI) (http.Handler, *session.Manager) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	store, err := session.NewStore(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := session.NewManager(session.Options{
		Client: api, Store: store, Username: "alice", Password: "secret",
		OTPTTL: time.Minute, OTPMaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	calc, err := voucher.New(5000)
	if err != nil {
		t.Fatal(err)
	}
	// The channel store is configured here so the notification routes are
	// registered: without it the drift test cannot see them, and undocumented
	// endpoints would slip through unnoticed.
	channels, err := notify.NewStore(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{
		Sessions: mgr, Client: api, Calculator: calc,
		RestaurantID: "31999", Metrics: metrics.New(), Version: "test",
		Channels: channels, Notifier: notify.NewNotifier(channels, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes(), mgr
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	return m
}

func TestHealthzIsAlwaysOK(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})
	if w := do(t, h, http.MethodGet, "/healthz", ""); w.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200", w.Code)
	}
}

func TestReadyzReflectsAuthentication(t *testing.T) {
	api := &fakeAPI{loginResult: &pluxee.LoginResult{Authenticated: true}}
	h, mgr := newServer(t, api)

	if w := do(t, h, http.MethodGet, "/readyz", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz while idle = %d, want 503", w.Code)
	}

	if _, err := mgr.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := do(t, h, http.MethodGet, "/readyz", ""); w.Code != http.StatusOK {
		t.Errorf("readyz once authenticated = %d, want 200", w.Code)
	}
}

func TestMetricsEndpointServesPrometheus(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})
	w := do(t, h, http.MethodGet, "/metrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("metrics = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "cubit_auth_state") {
		t.Error("metrics output does not include cubit_auth_state")
	}
}

func TestLoginReturnsTheMaskedTarget(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge()})

	w := do(t, h, http.MethodPost, "/api/v1/auth/login", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("login = %d, want 202", w.Code)
	}
	body := decode(t, w)
	if body["state"] != string(session.StateAwaitingOTP) {
		t.Errorf("state = %v", body["state"])
	}
	if body["masked_target"] != "05*-***1234" {
		t.Errorf("masked_target = %v", body["masked_target"])
	}
}

func TestSecondLoginIsConflict(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge()})
	do(t, h, http.MethodPost, "/api/v1/auth/login", "")

	w := do(t, h, http.MethodPost, "/api/v1/auth/login", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("second login = %d, want 409", w.Code)
	}
}

func TestLoginWithBadCredentialsIsUnauthorized(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginErr: pluxee.ErrInvalidCredentials})
	if w := do(t, h, http.MethodPost, "/api/v1/auth/login", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("login = %d, want 401", w.Code)
	}
}

func TestOTPSuccess(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge()})
	do(t, h, http.MethodPost, "/api/v1/auth/login", "")

	w := do(t, h, http.MethodPost, "/api/v1/auth/otp", `{"code":"123456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("otp = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if decode(t, w)["state"] != string(session.StateAuthenticated) {
		t.Error("state is not AUTHENTICATED after a good code")
	}
}

func TestOTPMalformedBodyIsBadRequest(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge()})
	do(t, h, http.MethodPost, "/api/v1/auth/login", "")

	for _, body := range []string{`not json`, `{}`, `{"code":""}`, `{"code":123}`} {
		w := do(t, h, http.MethodPost, "/api/v1/auth/otp", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("otp with body %q = %d, want 400", body, w.Code)
		}
	}
}

func TestOTPWrongCodeReportsAttemptsRemaining(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge(), otpErr: pluxee.ErrOTPRejected})
	do(t, h, http.MethodPost, "/api/v1/auth/login", "")

	w := do(t, h, http.MethodPost, "/api/v1/auth/otp", `{"code":"000000"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("otp = %d, want 401", w.Code)
	}
	body := decode(t, w)
	remaining, ok := body["attempts_remaining"].(float64)
	if !ok || remaining != 2 {
		t.Errorf("attempts_remaining = %v, want 2", body["attempts_remaining"])
	}
}

func TestOTPWithoutAChallengeIsConflict(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})
	w := do(t, h, http.MethodPost, "/api/v1/auth/otp", `{"code":"123456"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("otp with no challenge = %d, want 409", w.Code)
	}
}

func TestOTPAfterExpiryIsGone(t *testing.T) {
	api := &fakeAPI{loginResult: challenge()}
	key := make([]byte, 32)
	store, err := session.NewStore(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	mgr, err := session.NewManager(session.Options{
		Client: api, Store: store, Username: "a", Password: "b",
		OTPTTL: time.Minute, OTPMaxAttempts: 3,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	calc, _ := voucher.New(5000)
	h, err := New(Options{Sessions: mgr, Client: api, Calculator: calc,
		RestaurantID: "31999", Metrics: metrics.New()})
	if err != nil {
		t.Fatal(err)
	}
	routes := h.Routes()

	do(t, routes, http.MethodPost, "/api/v1/auth/login", "")
	now = now.Add(2 * time.Minute)

	w := do(t, routes, http.MethodPost, "/api/v1/auth/otp", `{"code":"123456"}`)
	if w.Code != http.StatusGone {
		t.Fatalf("otp after expiry = %d, want 410", w.Code)
	}
}

func TestStatusReportsState(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge()})

	w := do(t, h, http.MethodGet, "/api/v1/auth/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if decode(t, w)["state"] != string(session.StateIdle) {
		t.Error("state should start as IDLE")
	}

	do(t, h, http.MethodPost, "/api/v1/auth/login", "")
	body := decode(t, do(t, h, http.MethodGet, "/api/v1/auth/status", ""))
	if body["state"] != string(session.StateAwaitingOTP) {
		t.Errorf("state = %v, want AWAITING_OTP", body["state"])
	}
}

func TestBalanceUnauthenticatedIs503WithState(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})
	w := do(t, h, http.MethodGet, "/api/v1/balance", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("balance = %d, want 503", w.Code)
	}
	if decode(t, w)["state"] != string(session.StateIdle) {
		t.Error("the 503 body must carry the current state so the caller knows whether an OTP is pending")
	}
}

func TestBalanceReturnsTheDocumentedShape(t *testing.T) {
	api := &fakeAPI{loginResult: &pluxee.LoginResult{Authenticated: true}, balance: 27350}
	h, mgr := newServer(t, api)
	if _, err := mgr.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, http.MethodGet, "/api/v1/balance", "")
	if w.Code != http.StatusOK {
		t.Fatalf("balance = %d, want 200; body %s", w.Code, w.Body.String())
	}
	body := decode(t, w)

	want := map[string]any{
		"balance_agorot":       float64(27350),
		"balance":              "273.50",
		"currency":             "ILS",
		"voucher_value_agorot": float64(5000),
		"voucher_value":        "50.00",
		"vouchers_affordable":  float64(5),
		"remainder_agorot":     float64(2350),
		"remainder":            "23.50",
		"restaurant_id":        "31999",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}
	if _, ok := body["checked_at"].(string); !ok {
		t.Error("checked_at missing or not a string")
	}
}

func TestBalanceSessionExpiryDropsToIdle(t *testing.T) {
	api := &fakeAPI{
		loginResult: &pluxee.LoginResult{Authenticated: true},
		balanceErr:  pluxee.ErrSessionExpired,
	}
	h, mgr := newServer(t, api)
	if _, err := mgr.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, http.MethodGet, "/api/v1/balance", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("balance with a dead session = %d, want 503", w.Code)
	}
	if got := mgr.State(); got != session.StateIdle {
		t.Errorf("state = %q, want IDLE after the backend rejected the session", got)
	}
}

// No handler may ever echo a credential, code or token back to a caller.
func TestResponsesNeverEchoSecrets(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge(), otpErr: pluxee.ErrOTPRejected})
	do(t, h, http.MethodPost, "/api/v1/auth/login", "")

	w := do(t, h, http.MethodPost, "/api/v1/auth/otp", `{"code":"987654"}`)
	if strings.Contains(w.Body.String(), "987654") {
		t.Error("the response echoed the submitted OTP code")
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Error("the response echoed the password")
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{})
	if w := do(t, h, http.MethodGet, "/api/v1/nope", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown route = %d, want 404", w.Code)
	}
}

func TestOversizedOTPBodyIsRejected(t *testing.T) {
	h, _ := newServer(t, &fakeAPI{loginResult: challenge()})
	do(t, h, http.MethodPost, "/api/v1/auth/login", "")

	huge := `{"code":"` + strings.Repeat("1", 100000) + `"}`
	w := do(t, h, http.MethodPost, "/api/v1/auth/otp", huge)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 400 or 413", w.Code)
	}
}

func newRequest(method, path string) *http.Request {
	return httptest.NewRequest(method, path, nil)
}

func recorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func (f *fakeAPI) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutCalls++
	if f.logoutErr == nil {
		f.cookies = nil
	}
	return f.logoutErr
}
