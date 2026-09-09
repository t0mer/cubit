package session

import (
	"context"
	"testing"
)

// Posting credentials is the trigger: cubit records that a login is wanted, and
// a helper running alongside picks it up and drives the browser.

func TestSetCredentialsRecordsALoginRequest(t *testing.T) {
	m := newManagerNoCredentials(t, &fakeAPI{})

	if _, ok := m.TakeLoginRequest(); ok {
		t.Fatal("a login was wanted before any credentials arrived")
	}
	if err := m.SetCredentials("alice", "secret", "acme"); err != nil {
		t.Fatal(err)
	}

	req, ok := m.TakeLoginRequest()
	if !ok {
		t.Fatal("no login request after credentials were set")
	}
	if req.Username != "alice" || req.Password != "secret" || req.Company != "acme" {
		t.Errorf("request = %+v, want the credentials just set", struct{ U, C string }{req.Username, req.Company})
	}
}

// Single-use, like the OTP relay: the request carries a password, so a poll loop
// that could re-read it would keep re-exposing it.
func TestLoginRequestIsSingleUse(t *testing.T) {
	m := newManagerNoCredentials(t, &fakeAPI{})
	if err := m.SetCredentials("alice", "secret", ""); err != nil {
		t.Fatal(err)
	}

	if _, ok := m.TakeLoginRequest(); !ok {
		t.Fatal("first take found nothing")
	}
	if _, ok := m.TakeLoginRequest(); ok {
		t.Error("second take returned the credentials again")
	}
}

// Replacing credentials while a session is live should not ask anyone to log in
// again: there is nothing to log in for.
func TestNoLoginRequestWhileAuthenticated(t *testing.T) {
	api := &fakeAPI{loginResult: challengeResult()}
	m, _ := newManager(t, api)
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.SubmitOTP(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}

	if err := m.SetCredentials("bob", "hunter2", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.TakeLoginRequest(); ok {
		t.Error("a login was requested while a session was already held")
	}
}

// A logout should leave the machine ready to be driven again.
func TestLogoutDoesNotLeaveAStaleRequest(t *testing.T) {
	m := newManagerNoCredentials(t, &fakeAPI{})
	if err := m.SetCredentials("alice", "secret", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.TakeLoginRequest(); ok {
		t.Error("a stale login request survived a logout")
	}
}
