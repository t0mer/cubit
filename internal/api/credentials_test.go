package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/t0mer/cubit/internal/metrics"
	"github.com/t0mer/cubit/internal/session"
	"github.com/t0mer/cubit/internal/voucher"
)

// newCredentialsServer builds a server whose session manager starts without
// credentials, which is how the service comes up when they are meant to arrive
// over the API.
func newCredentialsServer(t *testing.T, token string) (http.Handler, *session.Manager, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{loginResult: challenge()}
	key := make([]byte, 32)
	store, err := session.NewStore(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := session.NewManager(session.Options{
		Client: api, Store: store,
		OTPTTL: time.Minute, OTPMaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	calc, err := voucher.New(5000)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{
		Sessions: mgr, Client: api, Calculator: calc,
		RestaurantID: "31999", Metrics: metrics.New(), Version: "test",
		APIToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes(), mgr, api
}

// post sends a JSON body carrying the API token.
func post(t *testing.T, h http.Handler, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("X-API-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const testToken = "0123456789abcdef01234567"

func TestCredentialsRefusedWhileTheAPIIsUnguarded(t *testing.T) {
	h, mgr, _ := newCredentialsServer(t, "")

	w := post(t, h, "", "/api/v1/auth/credentials",
		`{"username":"alice","password":"secret"}`)

	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412 on an unguarded instance", w.Code)
	}
	if mgr.HasCredentials() {
		t.Error("credentials were stored despite the refusal")
	}
}

func TestCredentialsRejectAnonymousCallersWhenGuarded(t *testing.T) {
	h, mgr, _ := newCredentialsServer(t, testToken)

	w := post(t, h, "", "/api/v1/auth/credentials",
		`{"username":"alice","password":"secret"}`)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if mgr.HasCredentials() {
		t.Error("credentials were stored for an unauthenticated caller")
	}
}

func TestCredentialsAreAcceptedAndDriveTheLogin(t *testing.T) {
	h, mgr, api := newCredentialsServer(t, testToken)

	w := post(t, h, testToken, "/api/v1/auth/credentials",
		`{"username":"alice","password":"secret","company":"acme"}`)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204", w.Code, w.Body.String())
	}
	if body := strings.TrimSpace(w.Body.String()); body != "" {
		t.Errorf("body = %q, want empty", body)
	}
	if !mgr.HasCredentials() {
		t.Fatal("credentials were not stored")
	}

	if lw := post(t, h, testToken, "/api/v1/auth/login", ""); lw.Code != http.StatusAccepted {
		t.Fatalf("login after setting credentials = %d, want 202", lw.Code)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.lastUser != "alice" {
		t.Errorf("backend saw username %q", api.lastUser)
	}
}

func TestCredentialsRejectAHalfSetPair(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	for _, body := range []string{
		`{"username":"","password":"secret"}`,
		`{"username":"alice","password":""}`,
		`{}`,
	} {
		if w := post(t, h, testToken, "/api/v1/auth/credentials", body); w.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, w.Code)
		}
	}
}

func TestCredentialsRejectMalformedJSON(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	if w := post(t, h, testToken, "/api/v1/auth/credentials", `{"username":`); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCredentialsRefusedWhileAwaitingOTP(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	if w := post(t, h, testToken, "/api/v1/auth/credentials",
		`{"username":"alice","password":"secret"}`); w.Code != http.StatusNoContent {
		t.Fatalf("setting credentials = %d", w.Code)
	}
	if w := post(t, h, testToken, "/api/v1/auth/login", ""); w.Code != http.StatusAccepted {
		t.Fatalf("login = %d", w.Code)
	}

	w := post(t, h, testToken, "/api/v1/auth/credentials",
		`{"username":"bob","password":"hunter2"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while a challenge is outstanding", w.Code)
	}
}

func TestCredentialsResponsesNeverEchoTheSecret(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)
	const password = "correct-horse-battery-staple"

	for _, w := range []*httptest.ResponseRecorder{
		post(t, h, testToken, "/api/v1/auth/credentials",
			`{"username":"alice","password":"`+password+`"}`),
		post(t, h, testToken, "/api/v1/auth/credentials",
			`{"username":"","password":"`+password+`"}`),
		post(t, h, "", "/api/v1/auth/credentials",
			`{"username":"alice","password":"`+password+`"}`),
	} {
		if strings.Contains(w.Body.String(), password) {
			t.Errorf("response echoed the password: %s", w.Body.String())
		}
	}
}

func TestAuthStatusReportsWhetherCredentialsAreConfigured(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	r.Header.Set("X-API-Token", testToken)
	h.ServeHTTP(w, r)
	if got := decode(t, w)["credentials_configured"]; got != false {
		t.Errorf("credentials_configured = %v, want false", got)
	}

	if cw := post(t, h, testToken, "/api/v1/auth/credentials",
		`{"username":"alice","password":"secret"}`); cw.Code != http.StatusNoContent {
		t.Fatalf("setting credentials = %d", cw.Code)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	r.Header.Set("X-API-Token", testToken)
	h.ServeHTTP(w, r)
	if got := decode(t, w)["credentials_configured"]; got != true {
		t.Errorf("credentials_configured = %v, want true", got)
	}
}

func TestLoginWithoutCredentialsPointsAtTheEndpoint(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	w := post(t, h, testToken, "/api/v1/auth/login", "")
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", w.Code)
	}
	if msg, _ := decode(t, w)["message"].(string); !strings.Contains(msg, "credentials") {
		t.Errorf("message %q should say credentials are missing", msg)
	}
}

func TestCredentialsRejectAnOversizedBody(t *testing.T) {
	h, mgr, _ := newCredentialsServer(t, testToken)

	huge := `{"username":"alice","password":"` + strings.Repeat("x", 64*1024) + `"}`
	if w := post(t, h, testToken, "/api/v1/auth/credentials", huge); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a body past the cap", w.Code)
	}
	if mgr.HasCredentials() {
		t.Error("an oversized body was stored anyway")
	}
}

func TestCredentialsRejectUnknownFields(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	body := `{"username":"alice","password":"secret","persist":true}`
	if w := post(t, h, testToken, "/api/v1/auth/credentials", body); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 so a misspelled or unsupported field is not silently ignored", w.Code)
	}
}

// The company field is optional, and the OpenAPI examples say so. This locks
// that in: it is a guard on documented behaviour, not a driver of new
// behaviour, so it passes the moment it is written.
func TestCredentialsAcceptedWithNoCompanyField(t *testing.T) {
	h, mgr, api := newCredentialsServer(t, testToken)

	w := post(t, h, testToken, "/api/v1/auth/credentials",
		`{"username":"0501234567","password":"secret"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204 without a company", w.Code, w.Body.String())
	}
	if !mgr.HasCredentials() {
		t.Fatal("credentials were not stored")
	}

	if lw := post(t, h, testToken, "/api/v1/auth/login", ""); lw.Code != http.StatusAccepted {
		t.Fatalf("login = %d, want 202", lw.Code)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.lastUser != "0501234567" {
		t.Errorf("backend saw username %q", api.lastUser)
	}
}
