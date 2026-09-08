package session

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/t0mer/cubit/internal/pluxee"
)

// fakeAPI is a scripted stand-in for the Pluxee backend.
type fakeAPI struct {
	mu sync.Mutex

	loginResult *pluxee.LoginResult
	loginErr    error
	loginCalls  int

	otpErr   error
	otpCalls int
	lastCode string

	balance     int64
	balanceErr  error
	balanceCall int

	cookies []*http.Cookie
}

func (f *fakeAPI) Login(context.Context, string, string, string) (*pluxee.LoginResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginCalls++
	return f.loginResult, f.loginErr
}

func (f *fakeAPI) SubmitOTP(_ context.Context, _ *pluxee.Challenge, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.otpCalls++
	f.lastCode = code
	if f.otpErr == nil {
		f.cookies = []*http.Cookie{{Name: "token", Value: "granted"}}
	}
	return f.otpErr
}

func (f *fakeAPI) Balance(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.balanceCall++
	return f.balance, f.balanceErr
}

func (f *fakeAPI) Session() []*http.Cookie { return f.cookies }

func (f *fakeAPI) RestoreSession(c []*http.Cookie) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cookies = c
	return nil
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func challengeResult() *pluxee.LoginResult {
	return &pluxee.LoginResult{Challenge: &pluxee.Challenge{
		UserInput1: "alice", MaskedInput: "05*-***1234", Method: "sms",
	}}
}

func newManager(t *testing.T, api *fakeAPI) (*Manager, *fakeClock) {
	t.Helper()
	store, err := NewStore(t.TempDir(), testKey(21))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	m, err := NewManager(Options{
		Client:         api,
		Store:          store,
		Username:       "alice",
		Password:       "secret",
		OTPTTL:         5 * time.Minute,
		OTPMaxAttempts: 3,
		Now:            clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, clock
}

func TestStartsIdle(t *testing.T) {
	m, _ := newManager(t, &fakeAPI{})
	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q", got, StateIdle)
	}
}

func TestLoginMovesToAwaitingOTP(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, _ := newManager(t, api)

	ch, err := m.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if ch.MaskedInput != "05*-***1234" {
		t.Errorf("MaskedInput = %q", ch.MaskedInput)
	}
	if got := m.State(); got != StateAwaitingOTP {
		t.Errorf("State = %q, want %q", got, StateAwaitingOTP)
	}
}

func TestSecondLoginWhileAwaitingOTPIsRejected(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, _ := newManager(t, api)

	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := m.Login(context.Background())
	if !errors.Is(err, ErrLoginInProgress) {
		t.Fatalf("second Login = %v, want ErrLoginInProgress", err)
	}
	if api.loginCalls != 1 {
		t.Fatalf("backend login called %d times; a second login must not trigger another OTP", api.loginCalls)
	}
}

func TestLoginStraightToAuthenticated(t *testing.T) {
	api := &fakeAPI{loginResult: &pluxee.LoginResult{Authenticated: true}}
	m, _ := newManager(t, api)

	ch, err := m.Login(context.Background())
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if ch != nil {
		t.Error("challenge returned even though no OTP was required")
	}
	if got := m.State(); got != StateAuthenticated {
		t.Errorf("State = %q, want %q", got, StateAuthenticated)
	}
}

func TestSubmitOTPAuthenticatesAndPersists(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, _ := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := m.SubmitOTP(context.Background(), "123456"); err != nil {
		t.Fatalf("SubmitOTP: %v", err)
	}
	if got := m.State(); got != StateAuthenticated {
		t.Errorf("State = %q, want %q", got, StateAuthenticated)
	}
	if api.lastCode != "123456" {
		t.Errorf("code forwarded = %q", api.lastCode)
	}

	persisted, err := m.store.Load()
	if err != nil {
		t.Fatalf("session was not persisted: %v", err)
	}
	if len(persisted.Cookies) == 0 {
		t.Error("persisted session holds no cookies")
	}
}

func TestSubmitOTPWithoutChallengeIsRejected(t *testing.T) {
	m, _ := newManager(t, &fakeAPI{})
	err := m.SubmitOTP(context.Background(), "123456")
	if !errors.Is(err, ErrNotAwaitingOTP) {
		t.Fatalf("SubmitOTP = %v, want ErrNotAwaitingOTP", err)
	}
}

func TestWrongOTPReportsAttemptsRemaining(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult(), otpErr: pluxee.ErrOTPRejected}
	m, _ := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	err := m.SubmitOTP(context.Background(), "000000")
	var otpErr *OTPRejectedError
	if !errors.As(err, &otpErr) {
		t.Fatalf("SubmitOTP = %v, want *OTPRejectedError", err)
	}
	if otpErr.Remaining != 2 {
		t.Errorf("Remaining = %d, want 2", otpErr.Remaining)
	}
	if got := m.State(); got != StateAwaitingOTP {
		t.Errorf("State = %q, want to stay in %q while attempts remain", got, StateAwaitingOTP)
	}
}

func TestExhaustingOTPAttemptsReturnsToIdle(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult(), otpErr: pluxee.ErrOTPRejected}
	m, _ := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := m.SubmitOTP(context.Background(), "000000"); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", i+1)
		}
	}
	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q after exhausting attempts", got, StateIdle)
	}

	// A fourth submission has no challenge to act on.
	if err := m.SubmitOTP(context.Background(), "000000"); !errors.Is(err, ErrNotAwaitingOTP) {
		t.Errorf("after exhaustion SubmitOTP = %v, want ErrNotAwaitingOTP", err)
	}
	if api.otpCalls != 3 {
		t.Errorf("backend saw %d otp submissions, want exactly 3", api.otpCalls)
	}
}

func TestExpiredChallengeIsRejectedAndClearsState(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, clock := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock.advance(5*time.Minute + time.Second)

	err := m.SubmitOTP(context.Background(), "123456")
	if !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("SubmitOTP after TTL = %v, want ErrChallengeExpired", err)
	}
	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q after expiry", got, StateIdle)
	}
	if api.otpCalls != 0 {
		t.Error("an expired challenge must not reach the backend")
	}
}

func TestChallengeJustInsideTTLStillWorks(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, clock := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	clock.advance(5*time.Minute - time.Second)

	if err := m.SubmitOTP(context.Background(), "123456"); err != nil {
		t.Fatalf("SubmitOTP just inside the TTL: %v", err)
	}
}

func TestRestoreValidSessionSkipsTheOTPDance(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, testKey(31))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&Persisted{Cookies: []*http.Cookie{{Name: "token", Value: "good"}}}); err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{balance: 27350}
	m, err := NewManager(Options{Client: api, Store: store, Username: "a", Password: "b"})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := m.State(); got != StateAuthenticated {
		t.Errorf("State = %q, want %q", got, StateAuthenticated)
	}
	if api.loginCalls != 0 {
		t.Error("Restore triggered a login even though the persisted session was good")
	}
}

func TestRestoreWithExpiredSessionStaysIdleAndClearsIt(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, testKey(32))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&Persisted{Cookies: []*http.Cookie{{Name: "token", Value: "stale"}}}); err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{balanceErr: pluxee.ErrSessionExpired}
	m, err := NewManager(Options{Client: api, Store: store, Username: "a", Password: "b"})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Restore with a dead session = %v, want ErrNoSession", err)
	}
	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q", got, StateIdle)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
		t.Error("a dead session was left on disk")
	}
}

func TestRestoreWithNoFileIsNotAnError(t *testing.T) {
	m, _ := newManager(t, &fakeAPI{})
	if err := m.Restore(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Restore = %v, want ErrNoSession", err)
	}
	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q", got, StateIdle)
	}
}

func TestInvalidateDropsTheSession(t *testing.T) {
	api := &fakeAPI{loginResult: &pluxee.LoginResult{Authenticated: true}}
	m, _ := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	m.Invalidate()

	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q", got, StateIdle)
	}
	if _, err := m.store.Load(); !errors.Is(err, ErrNoSession) {
		t.Error("Invalidate left the session on disk")
	}
}

func TestStatusReportsChallengeDetails(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, _ := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}

	st := m.Status()
	if st.State != StateAwaitingOTP {
		t.Errorf("State = %q", st.State)
	}
	if st.MaskedTarget != "05*-***1234" {
		t.Errorf("MaskedTarget = %q", st.MaskedTarget)
	}
	if st.AttemptsRemaining != 3 {
		t.Errorf("AttemptsRemaining = %d, want 3", st.AttemptsRemaining)
	}
	if st.ChallengeExpiresAt == nil {
		t.Error("ChallengeExpiresAt is nil while awaiting an OTP")
	}
}

func TestConcurrentLoginsTriggerExactlyOneOTP(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, _ := newManager(t, api)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Login(context.Background())
		}()
	}
	wg.Wait()

	if api.loginCalls != 1 {
		t.Fatalf("backend login called %d times under concurrency, want exactly 1", api.loginCalls)
	}
}
