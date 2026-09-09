package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getTok(t *testing.T, h http.Handler, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("X-API-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestBrowserLoginArmsTheRelay(t *testing.T) {
	h, mgr, api := newCredentialsServer(t, testToken)

	w := post(t, h, testToken, "/api/v1/auth/browser",
		`{"masked_target":"05*-***1865","method":"sms"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d (%s), want 202", w.Code, w.Body.String())
	}
	if got := mgr.State(); string(got) != "AWAITING_OTP" {
		t.Errorf("State = %q, want AWAITING_OTP", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.lastUser != "" {
		t.Error("arming the relay must not touch the pluxee backend")
	}
}

func TestBrowserLoginRefusedWhileAwaiting(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)
	post(t, h, testToken, "/api/v1/auth/browser", `{"masked_target":"05*-***1865"}`)

	w := post(t, h, testToken, "/api/v1/auth/browser", `{"masked_target":"05*-***1865"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestBrowserEndpointsRefuseWhileUnguarded(t *testing.T) {
	h, _, _ := newCredentialsServer(t, "")

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/auth/browser", `{"masked_target":"x"}`},
		{http.MethodPost, "/api/v1/auth/session", `{"cookies":[{"name":"t","value":"v"}]}`},
	} {
		w := post(t, h, "", c.path, c.body)
		if w.Code != http.StatusPreconditionFailed {
			t.Errorf("%s %s = %d, want 412 on an unguarded instance", c.method, c.path, w.Code)
		}
	}
	if w := getTok(t, h, "", "/api/v1/auth/browser/otp"); w.Code != http.StatusPreconditionFailed {
		t.Errorf("GET otp = %d, want 412", w.Code)
	}
}

// The phone posts to the unchanged /api/v1/auth/otp; the helper collects it.
func TestPhonePostsTheCodeAndTheHelperCollectsIt(t *testing.T) {
	h, _, api := newCredentialsServer(t, testToken)
	post(t, h, testToken, "/api/v1/auth/browser", `{"masked_target":"05*-***1865"}`)

	if w := getTok(t, h, testToken, "/api/v1/auth/browser/otp"); w.Code != http.StatusNoContent {
		t.Fatalf("poll before the code arrives = %d, want 204", w.Code)
	}

	if w := post(t, h, testToken, "/api/v1/auth/otp", `{"code":"123456"}`); w.Code != http.StatusAccepted {
		t.Fatalf("phone post = %d (%s), want 202", w.Code, w.Body.String())
	}
	api.mu.Lock()
	otpSeen := api.cookies != nil
	api.mu.Unlock()
	if otpSeen {
		t.Error("the code was submitted upstream; only the helper may do that")
	}

	w := getTok(t, h, testToken, "/api/v1/auth/browser/otp")
	if w.Code != http.StatusOK {
		t.Fatalf("collect = %d, want 200", w.Code)
	}
	if got := decode(t, w)["code"]; got != "123456" {
		t.Errorf("code = %v, want the parked code", got)
	}

	if w := getTok(t, h, testToken, "/api/v1/auth/browser/otp"); w.Code != http.StatusNoContent {
		t.Errorf("second collect = %d, want 204; the code is single-use", w.Code)
	}
}

func TestSessionImportAdoptsAWorkingSession(t *testing.T) {
	h, mgr, api := newCredentialsServer(t, testToken)
	api.mu.Lock()
	api.balance = 27350
	api.mu.Unlock()

	w := post(t, h, testToken, "/api/v1/auth/session",
		`{"cookies":[{"name":"token","value":"from-the-browser","domain":".pluxee.co.il","path":"/"}]}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204", w.Code, w.Body.String())
	}
	if got := mgr.State(); string(got) != "AUTHENTICATED" {
		t.Errorf("State = %q, want AUTHENTICATED", got)
	}
}

func TestSessionImportRejectsAnEmptyJar(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	for _, body := range []string{`{"cookies":[]}`, `{}`, `{"cookies":`} {
		if w := post(t, h, testToken, "/api/v1/auth/session", body); w.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, w.Code)
		}
	}
}

func TestSessionImportNeverEchoesACookieValue(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)
	const secret = "super-secret-cookie-value"

	for _, w := range []*httptest.ResponseRecorder{
		post(t, h, testToken, "/api/v1/auth/session",
			`{"cookies":[{"name":"token","value":"`+secret+`"}]}`),
		post(t, h, "", "/api/v1/auth/session",
			`{"cookies":[{"name":"token","value":"`+secret+`"}]}`),
	} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("response echoed a cookie value: %s", w.Body.String())
		}
	}
}
