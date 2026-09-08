package api

import (
	"net/http"
	"testing"

	"github.com/t0mer/cubit/internal/metrics"
	"github.com/t0mer/cubit/internal/session"
	"github.com/t0mer/cubit/internal/voucher"
)

func newGuardedServer(t *testing.T, token string) http.Handler {
	t.Helper()
	api := &fakeAPI{loginResult: challenge()}
	key := make([]byte, 32)
	store, err := session.NewStore(t.TempDir(), key)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := session.NewManager(session.Options{
		Client: api, Store: store, Username: "alice", Password: "secret",
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
		RestaurantID: "31999", Metrics: metrics.New(), APIToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes()
}

func request(t *testing.T, h http.Handler, method, path string, mutate func(*http.Request)) int {
	t.Helper()
	r := newRequest(method, path)
	if mutate != nil {
		mutate(r)
	}
	w := recorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// Guarded endpoints: everything that reads financial data or drives the state
// machine.
var guardedPaths = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/v1/balance"},
	{http.MethodGet, "/api/v1/auth/status"},
	{http.MethodPost, "/api/v1/auth/login"},
	{http.MethodPost, "/api/v1/auth/otp"},
	{http.MethodGet, "/metrics"},
}

func TestWithoutATokenTheAPIIsOpen(t *testing.T) {
	h := newGuardedServer(t, "")
	for _, ep := range guardedPaths {
		if code := request(t, h, ep.method, ep.path, nil); code == http.StatusUnauthorized {
			t.Errorf("%s %s = 401 with no token configured; bootstrap mode must stay open",
				ep.method, ep.path)
		}
	}
}

func TestWithATokenGuardedEndpointsRejectAnonymousCallers(t *testing.T) {
	h := newGuardedServer(t, "s3cret-token")
	for _, ep := range guardedPaths {
		if code := request(t, h, ep.method, ep.path, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d without a token, want 401", ep.method, ep.path, code)
		}
	}
}

func TestCorrectTokenHeaderIsAccepted(t *testing.T) {
	h := newGuardedServer(t, "s3cret-token")
	code := request(t, h, http.MethodGet, "/api/v1/auth/status", func(r *http.Request) {
		r.Header.Set("X-API-Token", "s3cret-token")
	})
	if code != http.StatusOK {
		t.Fatalf("status with a valid token = %d, want 200", code)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	h := newGuardedServer(t, "s3cret-token")
	for _, bad := range []string{"wrong", "", "s3cret-toke", "s3cret-tokenn", "S3CRET-TOKEN"} {
		code := request(t, h, http.MethodGet, "/api/v1/balance", func(r *http.Request) {
			r.Header.Set("X-API-Token", bad)
		})
		if code != http.StatusUnauthorized {
			t.Errorf("token %q = %d, want 401", bad, code)
		}
	}
}

func TestBasicAuthIsAcceptedAsAnAlternative(t *testing.T) {
	h := newGuardedServer(t, "s3cret-token")
	code := request(t, h, http.MethodGet, "/api/v1/auth/status", func(r *http.Request) {
		r.SetBasicAuth("cubit", "s3cret-token")
	})
	if code != http.StatusOK {
		t.Fatalf("status with valid basic auth = %d, want 200", code)
	}

	bad := request(t, h, http.MethodGet, "/api/v1/auth/status", func(r *http.Request) {
		r.SetBasicAuth("cubit", "nope")
	})
	if bad != http.StatusUnauthorized {
		t.Errorf("status with wrong basic auth = %d, want 401", bad)
	}
}

// Liveness and readiness are probes; an orchestrator cannot carry a token.
func TestHealthProbesStayOpenEvenWithAToken(t *testing.T) {
	h := newGuardedServer(t, "s3cret-token")
	for _, path := range []string{"/healthz", "/readyz"} {
		if code := request(t, h, http.MethodGet, path, nil); code == http.StatusUnauthorized {
			t.Errorf("%s = 401; health probes must stay reachable", path)
		}
	}
}

// A cross-origin form POST cannot set X-API-Token, so a configured token also
// closes the CSRF path that could trigger a real SMS.
func TestStateChangingRequestsRequireTheTokenHeaderNotAForm(t *testing.T) {
	h := newGuardedServer(t, "s3cret-token")
	code := request(t, h, http.MethodPost, "/api/v1/auth/login", func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("cross-origin style form POST = %d, want 401", code)
	}
}

func TestUnauthorizedResponseDoesNotEchoTheToken(t *testing.T) {
	h := newGuardedServer(t, "s3cret-token")
	r := newRequest(http.MethodGet, "/api/v1/balance")
	r.Header.Set("X-API-Token", "attacker-guess")
	w := recorder()
	h.ServeHTTP(w, r)

	body := w.Body.String()
	for _, leak := range []string{"s3cret-token", "attacker-guess"} {
		if contains(body, leak) {
			t.Errorf("401 body leaked %q: %s", leak, body)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
