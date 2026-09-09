package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t0mer/cubit/internal/metrics"
	"github.com/t0mer/cubit/internal/notify"
	"github.com/t0mer/cubit/internal/session"
	"github.com/t0mer/cubit/internal/voucher"
)

func newNotifyServer(t *testing.T, token string) (http.Handler, *notify.Store) {
	t.Helper()
	api := &fakeAPI{loginResult: challenge()}
	key := make([]byte, 32)
	dir := t.TempDir()
	store, err := session.NewStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := session.NewManager(session.Options{Client: api, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	channels, err := notify.NewStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	calc, _ := voucher.New(5000)
	h, err := New(Options{
		Sessions: mgr, Client: api, Calculator: calc, RestaurantID: "31999",
		Metrics: metrics.New(), APIToken: token,
		Channels: channels, Notifier: notify.NewNotifier(channels, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes(), channels
}

// newNotifyServerWithAPI is newNotifyServer plus access to the fake backend.
func newNotifyServerWithAPI(t *testing.T, token string) (http.Handler, *notify.Store, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{loginResult: challenge()}
	key := make([]byte, 32)
	dir := t.TempDir()
	store, err := session.NewStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := session.NewManager(session.Options{Client: api, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	channels, err := notify.NewStore(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	calc, _ := voucher.New(5000)
	h, err := New(Options{
		Sessions: mgr, Client: api, Calculator: calc, RestaurantID: "31999",
		Metrics: metrics.New(), APIToken: token,
		Channels: channels, Notifier: notify.NewNotifier(channels, nil, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.Routes(), channels, api
}

func req(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("X-API-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNotificationsStartEmpty(t *testing.T) {
	h, _ := newNotifyServer(t, testToken)
	w := req(t, h, http.MethodGet, "/api/v1/notifications", testToken, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Errorf("body = %q, want an empty array", got)
	}
}

func TestAddAndListAChannel(t *testing.T) {
	h, _ := newNotifyServer(t, testToken)

	w := req(t, h, http.MethodPost, "/api/v1/notifications", testToken, `{
		"name":"whatsapp","provider":"greenapi",
		"greenapi":{"instance_id":"7103","token":"secret-token","phone":"972501234567"},
		"enabled":true,"notify_on_success":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want 201", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-token") {
		t.Error("the create response echoed the provider token")
	}

	lw := req(t, h, http.MethodGet, "/api/v1/notifications", testToken, "")
	if !strings.Contains(lw.Body.String(), "whatsapp") {
		t.Errorf("list does not contain the channel: %s", lw.Body.String())
	}
	if strings.Contains(lw.Body.String(), "secret-token") {
		t.Error("the list leaked the provider token")
	}
}

func TestAddRejectsAnIncompleteChannel(t *testing.T) {
	h, _ := newNotifyServer(t, testToken)
	for _, body := range []string{
		`{"name":"x","provider":"greenapi","greenapi":{"instance_id":"1"}}`,
		`{"name":"","provider":"shoutrrr","shoutrrr":{"url":"gotify://h/t"}}`,
		`{"name":"x","provider":"pigeon"}`,
		`{`,
	} {
		if w := req(t, h, http.MethodPost, "/api/v1/notifications", testToken, body); w.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, w.Code)
		}
	}
}

func TestUpdateAndDeleteAChannel(t *testing.T) {
	h, store := newNotifyServer(t, testToken)
	ch, err := store.Add(notify.Channel{
		Name: "old", Provider: notify.ProviderShoutrrr,
		Shoutrrr: notify.ShoutrrrConfig{URL: "gotify://host/token"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	w := req(t, h, http.MethodPut, "/api/v1/notifications/"+ch.ID, testToken, `{
		"name":"renamed","provider":"shoutrrr",
		"shoutrrr":{"url":"gotify://host/token2"},"enabled":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d (%s), want 200", w.Code, w.Body.String())
	}
	got, _ := store.Get(ch.ID)
	if got.Name != "renamed" || got.Enabled {
		t.Errorf("channel = %+v, want the update applied", got)
	}

	dw := req(t, h, http.MethodDelete, "/api/v1/notifications/"+ch.ID, testToken, "")
	if dw.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", dw.Code)
	}
	if list, _ := store.List(); len(list) != 0 {
		t.Errorf("channel survived deletion")
	}
}

func TestUpdateAndDeleteUnknownChannel(t *testing.T) {
	h, _ := newNotifyServer(t, testToken)
	if w := req(t, h, http.MethodPut, "/api/v1/notifications/nope", testToken,
		`{"name":"x","provider":"shoutrrr","shoutrrr":{"url":"gotify://h/t"}}`); w.Code != http.StatusNotFound {
		t.Errorf("update = %d, want 404", w.Code)
	}
	if w := req(t, h, http.MethodDelete, "/api/v1/notifications/nope", testToken, ""); w.Code != http.StatusNotFound {
		t.Errorf("delete = %d, want 404", w.Code)
	}
}

// The guideline's Send Test button: fire a real message using the values in the
// request, before saving, so a user can validate config.
func TestSendTestUsesTheSuppliedValues(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	h, store := newNotifyServer(t, testToken)
	body := fmt.Sprintf(`{"name":"probe","provider":"greenapi",
		"greenapi":{"instance_id":"1","token":"t","phone":"9725","api_url":%q}}`, srv.URL)
	w := req(t, h, http.MethodPost, "/api/v1/notifications/test", testToken, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", w.Code, w.Body.String())
	}
	if hits != 1 {
		t.Errorf("provider received %d messages, want 1", hits)
	}
	if list, _ := store.List(); len(list) != 0 {
		t.Error("the test saved the channel; it must only send")
	}
}

func TestSendTestReportsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h, _ := newNotifyServer(t, testToken)
	body := fmt.Sprintf(`{"name":"probe","provider":"greenapi",
		"greenapi":{"instance_id":"1","token":"t","phone":"9725","api_url":%q}}`, srv.URL)
	w := req(t, h, http.MethodPost, "/api/v1/notifications/test", testToken, body)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestNotificationsRequireTheToken(t *testing.T) {
	h, _ := newNotifyServer(t, testToken)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/notifications"},
		{http.MethodPost, "/api/v1/notifications"},
		{http.MethodPost, "/api/v1/notifications/test"},
	} {
		if w := req(t, h, c.method, c.path, "", `{}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, w.Code)
		}
	}
}

// The guideline's rule: after every run, tell the channels that asked.
func TestABalanceCheckNotifiesSuccessChannels(t *testing.T) {
	var got []string
	var mu sync.Mutex
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(raw))
		mu.Unlock()
		fmt.Fprint(w, `{}`)
	}))
	defer provider.Close()

	h, store, api := newNotifyServerWithAPI(t, testToken)
	api.mu.Lock()
	api.balance = 68600
	api.cookies = []*http.Cookie{{Name: "t", Value: "v"}}
	api.mu.Unlock()
	if _, err := store.Add(notify.Channel{
		Name: "wa", Provider: notify.ProviderGreenAPI,
		GreenAPI:        notify.GreenAPIConfig{InstanceID: "1", Token: "t", Phone: "9725", APIURL: provider.URL},
		Enabled:         true,
		NotifyOnSuccess: true,
	}); err != nil {
		t.Fatal(err)
	}

	if w := req(t, h, http.MethodPost, "/api/v1/auth/session", testToken,
		`{"cookies":[{"name":"t","value":"v"}]}`); w.Code != http.StatusNoContent {
		t.Fatalf("setup: session import = %d", w.Code)
	}
	if w := req(t, h, http.MethodGet, "/api/v1/balance", testToken, ""); w.Code != http.StatusOK {
		t.Fatalf("balance = %d", w.Code)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("a successful balance check sent no notification")
	}
	if !strings.Contains(got[0], "686.00") {
		t.Errorf("message does not carry the balance: %s", got[0])
	}
	if !strings.Contains(got[0], "13") {
		t.Errorf("message does not carry the voucher count: %s", got[0])
	}
}

// A broken provider must never cost the caller their balance.
func TestABrokenChannelDoesNotBreakTheBalanceCall(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()

	h, store, api := newNotifyServerWithAPI(t, testToken)
	api.mu.Lock()
	api.balance = 68600
	api.mu.Unlock()
	store.Add(notify.Channel{
		Name: "dead", Provider: notify.ProviderGreenAPI,
		GreenAPI:        notify.GreenAPIConfig{InstanceID: "1", Token: "t", Phone: "9725", APIURL: dead.URL},
		Enabled:         true,
		NotifyOnSuccess: true,
	})
	req(t, h, http.MethodPost, "/api/v1/auth/session", testToken,
		`{"cookies":[{"name":"t","value":"v"}]}`)

	w := req(t, h, http.MethodGet, "/api/v1/balance", testToken, "")
	if w.Code != http.StatusOK {
		t.Fatalf("balance = %d, want 200 despite the dead channel", w.Code)
	}
	if !strings.Contains(w.Body.String(), "686.00") {
		t.Errorf("balance body = %s", w.Body.String())
	}
}

// Closing the loop: once a login completes, cubit reads the balance by itself
// so the user gets their notification without having to call anything.
func TestASuccessfulLoginTriggersABalanceCheck(t *testing.T) {
	var hits int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{}`)
	}))
	defer provider.Close()

	h, store, api := newNotifyServerWithAPI(t, testToken)
	api.mu.Lock()
	api.balance = 68600
	api.mu.Unlock()
	store.Add(notify.Channel{
		Name: "wa", Provider: notify.ProviderGreenAPI,
		GreenAPI:        notify.GreenAPIConfig{InstanceID: "1", Token: "t", Phone: "9725", APIURL: provider.URL},
		Enabled:         true,
		NotifyOnSuccess: true,
	})

	// A session handed over by the helper is a completed login.
	if w := req(t, h, http.MethodPost, "/api/v1/auth/session", testToken,
		`{"cookies":[{"name":"t","value":"v"}]}`); w.Code != http.StatusNoContent {
		t.Fatalf("session import = %d", w.Code)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&hits) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("a completed login sent no balance notification")
	}
}
