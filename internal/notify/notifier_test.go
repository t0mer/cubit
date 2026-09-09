package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// recorder stands in for a provider endpoint and counts what arrives.
type recorder struct {
	mu       sync.Mutex
	messages []string
	status   int
}

func (r *recorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.messages = append(r.messages, string(raw))
		r.mu.Unlock()
		if r.status != 0 {
			w.WriteHeader(r.status)
		}
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

func newNotifier(t *testing.T) (*Notifier, *Store) {
	t.Helper()
	s, _ := newStore(t)
	return NewNotifier(s, nil, nil), s
}

func greenChannel(url, name string, enabled, onSuccess, onFailure bool) Channel {
	return Channel{
		Name: name, Provider: ProviderGreenAPI,
		GreenAPI:        GreenAPIConfig{InstanceID: "1", Token: "t", Phone: "9725", APIURL: url},
		Enabled:         enabled,
		NotifyOnSuccess: onSuccess,
		NotifyOnFailure: onFailure,
	}
}

func TestSuccessGoesOnlyToSuccessChannels(t *testing.T) {
	wantIt := &recorder{}
	notWantIt := &recorder{}
	n, s := newNotifier(t)
	if _, err := s.Add(greenChannel(wantIt.server(t).URL, "yes", true, true, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(greenChannel(notWantIt.server(t).URL, "no", true, false, true)); err != nil {
		t.Fatal(err)
	}

	n.Notify(context.Background(), Success, "balance is 686.00")

	if wantIt.count() != 1 {
		t.Errorf("success channel got %d messages, want 1", wantIt.count())
	}
	if notWantIt.count() != 0 {
		t.Errorf("failure-only channel got %d messages, want 0", notWantIt.count())
	}
}

func TestFailureGoesOnlyToFailureChannels(t *testing.T) {
	onFail := &recorder{}
	onOK := &recorder{}
	n, s := newNotifier(t)
	s.Add(greenChannel(onFail.server(t).URL, "fail", true, false, true))
	s.Add(greenChannel(onOK.server(t).URL, "ok", true, true, false))

	n.Notify(context.Background(), Failure, "could not read the balance")

	if onFail.count() != 1 {
		t.Errorf("failure channel got %d, want 1", onFail.count())
	}
	if onOK.count() != 0 {
		t.Errorf("success-only channel got %d, want 0", onOK.count())
	}
}

func TestDisabledChannelsAreSkipped(t *testing.T) {
	r := &recorder{}
	n, s := newNotifier(t)
	s.Add(greenChannel(r.server(t).URL, "off", false, true, true))

	n.Notify(context.Background(), Success, "hello")

	if r.count() != 0 {
		t.Errorf("a disabled channel got %d messages", r.count())
	}
}

// Sending is best effort: the guideline says it must never block or fail the
// primary operation. A provider returning 500 must not stop the others.
func TestAFailingChannelDoesNotStopTheRest(t *testing.T) {
	broken := &recorder{status: http.StatusInternalServerError}
	working := &recorder{}
	n, s := newNotifier(t)
	s.Add(greenChannel(broken.server(t).URL, "broken", true, true, false))
	s.Add(greenChannel(working.server(t).URL, "working", true, true, false))

	n.Notify(context.Background(), Success, "hello")

	if working.count() != 1 {
		t.Errorf("the working channel got %d messages, want 1", working.count())
	}
}

func TestNotifyWithNoChannelsIsHarmless(t *testing.T) {
	n, _ := newNotifier(t)
	n.Notify(context.Background(), Success, "nobody is listening")
}

// The caller is an HTTP handler, so this must not hang on a dead endpoint.
func TestNotifyRespectsItsDeadline(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer slow.Close()

	n, s := newNotifier(t)
	s.Add(greenChannel(slow.URL, "slow", true, true, false))

	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		n.Notify(ctx, Success, "hello")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify ignored its context and blocked")
	}
}
