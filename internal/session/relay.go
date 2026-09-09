package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/t0mer/cubit/internal/pluxee"
)

// ErrSessionNotUsable means a session handed to AdoptSession does not work.
// Storing it would leave the service looking authenticated while every balance
// read failed, so it is rejected instead. Mapped to 400.
var ErrSessionNotUsable = errors.New("session: the supplied session is not usable")

// ErrAlreadyAuthenticated means a login was started while a usable session is
// already held. Arming the relay would move the machine out of AUTHENTICATED
// and strand a session that still works, so it is refused. Mapped to 409.
var ErrAlreadyAuthenticated = errors.New("session: already authenticated")

// ExpectExternalOTP arms the machine for a login being driven elsewhere.
//
// Pluxee enforces reCAPTCHA on both halves of its login — the credential post
// and the OTP submission — and a token can only be minted by a real browser.
// So a browser-assisted login lives in the cubit-login helper, which holds the
// challenge. Cubit's only job is to catch the code the user's phone posts to
// /api/v1/auth/otp and hand it to that helper.
//
// Nothing here touches the backend: there is no challenge to submit against.
func (m *Manager) ExpectExternalOTP(maskedTarget, method string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireChallengeLocked()

	if m.state == StateAwaitingOTP {
		return ErrLoginInProgress
	}
	// A held session is worth more than a new login: discarding it here would
	// force an OTP the user did not need. Invalidate deliberately if you really
	// want to start over.
	if m.state == StateAuthenticated {
		return ErrAlreadyAuthenticated
	}

	m.state = StateAwaitingOTP
	m.external = true
	m.externalCode = ""
	m.challenge = &pluxee.Challenge{MaskedInput: maskedTarget, Method: method}
	m.challengeExpiry = m.now().Add(m.otpTTL)
	m.attemptsLeft = m.maxAttempts
	m.log.Info("awaiting an otp for a browser-assisted login",
		"masked_target", maskedTarget, "method", method, "expires_at", m.challengeExpiry)
	return nil
}

// CollectExternalOTP hands the parked code to the helper, exactly once.
//
// Single-use on purpose: the code is a live credential for the few minutes it
// lives, and a poll loop that could re-read it would keep re-exposing it.
func (m *Manager) CollectExternalOTP() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireChallengeLocked()

	if !m.external || m.externalCode == "" {
		return "", false
	}
	code := m.externalCode
	m.externalCode = ""
	return code, true
}

// AdoptSession takes cookies captured by the helper and, if they actually work,
// becomes authenticated with them.
//
// The session is validated before it is trusted, exactly as Restore validates
// what it reads from disk: the only way to know a cookie is good is to use it.
func (m *Manager) AdoptSession(ctx context.Context, cookies []*http.Cookie) error {
	if len(cookies) == 0 {
		return fmt.Errorf("session: no cookies supplied")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.client.RestoreSession(cookies); err != nil {
		return fmt.Errorf("restoring supplied cookies: %w", err)
	}

	if _, err := m.client.Balance(ctx); err != nil {
		_ = m.client.RestoreSession(nil)
		m.resetLocked()
		if errors.Is(err, pluxee.ErrSessionExpired) {
			return ErrSessionNotUsable
		}
		return fmt.Errorf("validating the supplied session: %w", err)
	}

	m.state = StateAuthenticated
	m.external = false
	m.externalCode = ""
	m.challenge = nil
	m.challengeExpiry = time.Time{}
	m.authenticatedAt = m.now()
	m.persistLocked()
	m.log.Info("adopted a session from a browser-assisted login")
	return nil
}
