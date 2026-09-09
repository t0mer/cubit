package api

import (
	"fmt"
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

// A real cookie jar is far bigger than the 4 KiB the OTP endpoint allows: the
// live login produced 19 cookies, several of them long tokens. Capping the
// import at the OTP size rejects every genuine session.
func TestSessionImportAcceptsARealisticCookieJar(t *testing.T) {
	h, mgr, api := newCredentialsServer(t, testToken)
	api.mu.Lock()
	api.balance = 27350
	api.mu.Unlock()

	var b strings.Builder
	b.WriteString(`{"cookies":[`)
	for i := 0; i < 19; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":"c%d","value":"%s","domain":".pluxee.co.il","path":"/"}`,
			i, strings.Repeat("t", 900))
	}
	b.WriteString(`]}`)
	if b.Len() < 8<<10 {
		t.Fatalf("test jar is only %d bytes; it must exceed the old 4 KiB cap", b.Len())
	}

	w := post(t, h, testToken, "/api/v1/auth/session", b.String())
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204 for a %d-byte jar", w.Code, w.Body.String(), b.Len())
	}
	if got := mgr.State(); string(got) != "AUTHENTICATED" {
		t.Errorf("State = %q, want AUTHENTICATED", got)
	}
}

func TestBrowserLoginRefusedWhileAuthenticated(t *testing.T) {
	h, _, api := newCredentialsServer(t, testToken)
	api.mu.Lock()
	api.balance = 27350
	api.mu.Unlock()

	if w := post(t, h, testToken, "/api/v1/auth/session",
		`{"cookies":[{"name":"t","value":"v"}]}`); w.Code != http.StatusNoContent {
		t.Fatalf("setup: session import = %d", w.Code)
	}

	w := post(t, h, testToken, "/api/v1/auth/browser", `{"masked_target":"x"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 rather than discarding a live session", w.Code)
	}
	if bw := getTok(t, h, testToken, "/api/v1/balance"); bw.Code != http.StatusOK {
		t.Errorf("balance = %d after the refused arm; the session must still work", bw.Code)
	}
}

func TestLogoutClearsAnAuthenticatedSession(t *testing.T) {
	h, mgr, api := newCredentialsServer(t, testToken)
	api.mu.Lock()
	api.balance = 27350
	api.mu.Unlock()

	if w := post(t, h, testToken, "/api/v1/auth/session",
		`{"cookies":[{"name":"t","value":"v"}]}`); w.Code != http.StatusNoContent {
		t.Fatalf("setup: session import = %d", w.Code)
	}

	w := post(t, h, testToken, "/api/v1/auth/logout", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s), want 204", w.Code, w.Body.String())
	}
	if got := mgr.State(); string(got) != "IDLE" {
		t.Errorf("State = %q, want IDLE", got)
	}
	api.mu.Lock()
	calls := api.logoutCalls
	api.mu.Unlock()
	if calls != 1 {
		t.Errorf("upstream logout called %d times, want 1", calls)
	}
	if bw := getTok(t, h, testToken, "/api/v1/balance"); bw.Code != http.StatusServiceUnavailable {
		t.Errorf("balance after logout = %d, want 503", bw.Code)
	}
}

func TestLogoutIsIdempotent(t *testing.T) {
	h, _, api := newCredentialsServer(t, testToken)

	if w := post(t, h, testToken, "/api/v1/auth/logout", ""); w.Code != http.StatusNoContent {
		t.Fatalf("logout while idle = %d, want 204", w.Code)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.logoutCalls != 0 {
		t.Errorf("upstream logout called %d times while idle; there is nothing to revoke", api.logoutCalls)
	}
}

func TestLogoutRequiresTheToken(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)
	if w := post(t, h, "", "/api/v1/auth/logout", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestLoginRequestHandedToAWaitingHelper(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)

	if w := getTok(t, h, testToken, "/api/v1/auth/login-request"); w.Code != http.StatusNoContent {
		t.Fatalf("poll before credentials = %d, want 204", w.Code)
	}

	if w := post(t, h, testToken, "/api/v1/auth/credentials",
		`{"username":"alice","password":"secret","company":"acme"}`); w.Code != http.StatusNoContent {
		t.Fatalf("setting credentials = %d", w.Code)
	}

	w := getTok(t, h, testToken, "/api/v1/auth/login-request")
	if w.Code != http.StatusOK {
		t.Fatalf("poll after credentials = %d, want 200", w.Code)
	}
	got := decode(t, w)
	if got["username"] != "alice" || got["password"] != "secret" || got["company"] != "acme" {
		t.Errorf("body = %v, want the credentials just set", got)
	}

	if w := getTok(t, h, testToken, "/api/v1/auth/login-request"); w.Code != http.StatusNoContent {
		t.Errorf("second poll = %d, want 204; the request is single-use", w.Code)
	}
}

func TestLoginRequestRefusedWhileUnguarded(t *testing.T) {
	h, _, _ := newCredentialsServer(t, "")
	if w := getTok(t, h, "", "/api/v1/auth/login-request"); w.Code != http.StatusPreconditionFailed {
		t.Errorf("status = %d, want 412 on an unguarded instance", w.Code)
	}
}

func TestLoginRequestRequiresTheToken(t *testing.T) {
	h, _, _ := newCredentialsServer(t, testToken)
	post(t, h, testToken, "/api/v1/auth/credentials", `{"username":"a","password":"b"}`)
	if w := getTok(t, h, "", "/api/v1/auth/login-request"); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
