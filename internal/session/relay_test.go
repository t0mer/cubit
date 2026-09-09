package session

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A browser-assisted login runs in a helper that holds the Pluxee challenge and
// can mint the reCAPTCHA token that submitting the code requires. Cubit's job
// is only to catch the code the phone posts and hand it over, so nothing here
// may reach the backend.

func TestExpectExternalOTPMovesToAwaiting(t *testing.T) {
	api := &fakeAPI{}
	m, _ := newManager(t, api)

	if err := m.ExpectExternalOTP("05*-***1865", "sms"); err != nil {
		t.Fatalf("ExpectExternalOTP: %v", err)
	}
	if got := m.State(); got != StateAwaitingOTP {
		t.Errorf("State = %q, want %q", got, StateAwaitingOTP)
	}
	st := m.Status()
	if st.MaskedTarget != "05*-***1865" {
		t.Errorf("MaskedTarget = %q", st.MaskedTarget)
	}
	if api.loginCalls != 0 {
		t.Errorf("the backend was called %d time(s); the helper owns the login", api.loginCalls)
	}
}

func TestExpectExternalOTPRefusedWhileAlreadyAwaiting(t *testing.T) {
	m, _ := newManager(t, &fakeAPI{})
	if err := m.ExpectExternalOTP("05*-***1865", "sms"); err != nil {
		t.Fatal(err)
	}
	if err := m.ExpectExternalOTP("05*-***1865", "sms"); !errors.Is(err, ErrLoginInProgress) {
		t.Fatalf("error = %v, want ErrLoginInProgress", err)
	}
}

// The code posted by the phone is parked, never sent upstream: cubit cannot
// mint the captcha token the submission needs.
func TestSubmitOTPParksTheCodeInExternalMode(t *testing.T) {
	api := &fakeAPI{}
	m, _ := newManager(t, api)
	if err := m.ExpectExternalOTP("05*-***1865", "sms"); err != nil {
		t.Fatal(err)
	}

	if err := m.SubmitOTP(context.Background(), "123456"); err != nil {
		t.Fatalf("SubmitOTP: %v", err)
	}
	if api.otpCalls != 0 {
		t.Errorf("the backend saw %d otp submission(s); it must see none", api.otpCalls)
	}
	if got := m.State(); got != StateAwaitingOTP {
		t.Errorf("State = %q, want to stay %q until the helper finishes", got, StateAwaitingOTP)
	}
}

// The helper collects the code exactly once. A second read gets nothing, so a
// replayed poll cannot re-expose it.
func TestCollectExternalOTPIsSingleUse(t *testing.T) {
	m, _ := newManager(t, &fakeAPI{})
	if err := m.ExpectExternalOTP("05*-***1865", "sms"); err != nil {
		t.Fatal(err)
	}

	if code, ok := m.CollectExternalOTP(); ok {
		t.Fatalf("got code %q before the phone posted one", code)
	}
	if err := m.SubmitOTP(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}

	code, ok := m.CollectExternalOTP()
	if !ok || code != "123456" {
		t.Fatalf("CollectExternalOTP = %q, %v; want the parked code", code, ok)
	}
	if code, ok := m.CollectExternalOTP(); ok {
		t.Errorf("a second collect returned %q; the code must be single-use", code)
	}
}

func TestExternalChallengeExpires(t *testing.T) {
	m, clock := newManager(t, &fakeAPI{})
	if err := m.ExpectExternalOTP("05*-***1865", "sms"); err != nil {
		t.Fatal(err)
	}
	clock.advance(6 * time.Minute)

	if err := m.SubmitOTP(context.Background(), "123456"); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("error = %v, want ErrChallengeExpired", err)
	}
	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q", got, StateIdle)
	}
}

// A session captured by the helper is adopted only if it actually works.
func TestAdoptSessionAcceptsAWorkingSession(t *testing.T) {
	api := &fakeAPI{balance: 27350}
	m, _ := newManager(t, api)

	cookies := []*http.Cookie{{Name: "token", Value: "from-the-browser"}}
	if err := m.AdoptSession(context.Background(), cookies); err != nil {
		t.Fatalf("AdoptSession: %v", err)
	}
	if got := m.State(); got != StateAuthenticated {
		t.Errorf("State = %q, want %q", got, StateAuthenticated)
	}
	if _, err := m.store.Load(); err != nil {
		t.Errorf("the adopted session was not persisted: %v", err)
	}
}

func TestAdoptSessionRejectsADeadSession(t *testing.T) {
	api := &fakeAPI{balanceErr: errSessionExpiredForTest()}
	m, _ := newManager(t, api)

	err := m.AdoptSession(context.Background(), []*http.Cookie{{Name: "token", Value: "stale"}})
	if !errors.Is(err, ErrSessionNotUsable) {
		t.Fatalf("error = %v, want ErrSessionNotUsable", err)
	}
	if got := m.State(); got != StateIdle {
		t.Errorf("State = %q, want %q", got, StateIdle)
	}
	if _, err := m.store.Load(); !errors.Is(err, ErrNoSession) {
		t.Error("a dead session was persisted anyway")
	}
}

func TestAdoptSessionRejectsNoCookies(t *testing.T) {
	m, _ := newManager(t, &fakeAPI{})
	if err := m.AdoptSession(context.Background(), nil); err == nil {
		t.Fatal("AdoptSession accepted an empty cookie jar")
	}
}

// Arming the relay while a good session is held used to discard it: the state
// moved to AWAITING_OTP, so the balance started failing even though the cookies
// were still valid. A stray call must not cost a working session.
func TestExpectExternalOTPRefusedWhileAuthenticated(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, _ := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.SubmitOTP(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}
	if got := m.State(); got != StateAuthenticated {
		t.Fatalf("setup failed: state = %q", got)
	}

	if err := m.ExpectExternalOTP("05*-***1865", "sms"); !errors.Is(err, ErrAlreadyAuthenticated) {
		t.Fatalf("error = %v, want ErrAlreadyAuthenticated", err)
	}
	if got := m.State(); got != StateAuthenticated {
		t.Errorf("state = %q; the live session must survive", got)
	}
}
