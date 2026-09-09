// Package session owns the authentication state machine and the encrypted
// persistence of the Pluxee session.
//
// The state machine exists because logging in needs a human: Pluxee sends an OTP
// out of band, so the service parks in AWAITING_OTP until someone posts the code.
//
//	IDLE ──login──> AWAITING_OTP ──otp──> AUTHENTICATED
//	  ^                  │                      │
//	  └── ttl / attempts ┘   ── 401 / expiry ───┘
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/t0mer/cubit/internal/pluxee"
)

// State is a state of the authentication machine.
type State string

const (
	// StateIdle means no login is in flight and no session is held.
	StateIdle State = "IDLE"
	// StateAwaitingOTP means an OTP was sent and the service is waiting for it.
	StateAwaitingOTP State = "AWAITING_OTP"
	// StateAuthenticated means a usable session is held.
	StateAuthenticated State = "AUTHENTICATED"
)

// Defaults for the OTP challenge, per the build contract.
const (
	DefaultOTPTTL         = 5 * time.Minute
	DefaultOTPMaxAttempts = 3
)

// Errors the HTTP layer maps onto status codes.
var (
	// ErrLoginInProgress means a challenge is already outstanding. Mapped to 409.
	// Starting a second login would send the user a second SMS.
	ErrLoginInProgress = errors.New("session: a login is already in progress")
	// ErrNotAwaitingOTP means there is no challenge to answer. Mapped to 409.
	ErrNotAwaitingOTP = errors.New("session: not awaiting an otp")
	// ErrChallengeExpired means the challenge outlived its TTL. Mapped to 410.
	ErrChallengeExpired = errors.New("session: otp challenge expired")
	// ErrNoCredentials means no Pluxee username and password are held, either
	// from configuration or from the API. Mapped to 412.
	ErrNoCredentials = errors.New("session: pluxee credentials are not configured")
)

// OTPRejectedError is a wrong code, carrying how many attempts are left.
// Mapped to 401.
type OTPRejectedError struct {
	Remaining int
}

func (e *OTPRejectedError) Error() string {
	return fmt.Sprintf("session: otp rejected, %d attempt(s) remaining", e.Remaining)
}

// Status is a snapshot of the machine, safe to serialise to a client. It carries
// no code, no credential and no token.
type Status struct {
	State              State      `json:"state"`
	MaskedTarget       string     `json:"masked_target,omitempty"`
	DeliveryMethod     string     `json:"delivery_method,omitempty"`
	AttemptsRemaining  int        `json:"attempts_remaining,omitempty"`
	ChallengeExpiresAt *time.Time `json:"challenge_expires_at,omitempty"`
	AuthenticatedAt    *time.Time `json:"authenticated_at,omitempty"`
	// CredentialsConfigured says whether a login could be attempted at all.
	CredentialsConfigured bool `json:"credentials_configured"`
}

// Options configures a Manager.
type Options struct {
	Client         pluxee.API
	Store          *Store
	Username       string
	Password       string
	Company        string
	OTPTTL         time.Duration
	OTPMaxAttempts int
	Now            func() time.Time
	Logger         *slog.Logger
}

// Manager drives the login state machine and persists the resulting session.
//
// Every method is safe for concurrent use: the whole machine sits behind one
// mutex, because the invariant that matters most is that exactly one login can
// be in flight at a time.
type Manager struct {
	mu    sync.Mutex
	state State

	challenge       *pluxee.Challenge
	challengeExpiry time.Time
	attemptsLeft    int
	authenticatedAt time.Time

	client   pluxee.API
	store    *Store
	username string
	password string
	company  string

	otpTTL      time.Duration
	maxAttempts int
	now         func() time.Time
	log         *slog.Logger
}

// NewManager returns a Manager in the IDLE state.
func NewManager(opts Options) (*Manager, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("session: a pluxee client is required")
	}
	if opts.Store == nil {
		return nil, fmt.Errorf("session: a store is required")
	}

	m := &Manager{
		state:       StateIdle,
		client:      opts.Client,
		store:       opts.Store,
		username:    strings.TrimSpace(opts.Username),
		password:    opts.Password,
		company:     opts.Company,
		otpTTL:      opts.OTPTTL,
		maxAttempts: opts.OTPMaxAttempts,
		now:         opts.Now,
		log:         opts.Logger,
	}
	if m.otpTTL <= 0 {
		m.otpTTL = DefaultOTPTTL
	}
	if m.maxAttempts <= 0 {
		m.maxAttempts = DefaultOTPMaxAttempts
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	return m, nil
}

// State returns the current state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireChallengeLocked()
	return m.state
}

// Status returns a snapshot of the machine.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireChallengeLocked()

	st := Status{
		State:                 m.state,
		CredentialsConfigured: m.username != "" && m.password != "",
	}
	if m.state == StateAwaitingOTP && m.challenge != nil {
		expiry := m.challengeExpiry
		st.MaskedTarget = m.challenge.MaskedInput
		st.DeliveryMethod = m.challenge.Method
		st.AttemptsRemaining = m.attemptsLeft
		st.ChallengeExpiresAt = &expiry
	}
	if m.state == StateAuthenticated && !m.authenticatedAt.IsZero() {
		at := m.authenticatedAt
		st.AuthenticatedAt = &at
	}
	return st
}

// Restore loads a persisted session and validates it against the backend.
//
// A session that still works puts the machine straight into AUTHENTICATED, which
// is what makes the service usable day to day: no OTP on every restart. A
// session that no longer works is deleted and ErrNoSession is returned.
func (m *Manager) Restore(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	persisted, err := m.store.Load()
	if err != nil {
		if !errors.Is(err, ErrNoSession) {
			m.log.Warn("could not read the persisted session, starting fresh", "error", err)
		}
		return ErrNoSession
	}

	if err := m.client.RestoreSession(persisted.Cookies); err != nil {
		return fmt.Errorf("restoring session cookies: %w", err)
	}

	// The only way to know a cookie is still good is to use it.
	if _, err := m.client.Balance(ctx); err != nil {
		if errors.Is(err, pluxee.ErrSessionExpired) {
			m.log.Info("persisted session is no longer valid, discarding it")
			_ = m.store.Clear()
			_ = m.client.RestoreSession(nil)
			return ErrNoSession
		}
		return fmt.Errorf("validating persisted session: %w", err)
	}

	m.state = StateAuthenticated
	m.authenticatedAt = m.now()
	m.log.Info("restored a valid session from disk, no otp needed",
		"saved_at", persisted.SavedAt)
	return nil
}

// Login begins a password login.
//
// It returns the pending challenge, or nil if the backend authenticated us
// outright. A login started while another is outstanding returns
// ErrLoginInProgress rather than silently triggering a second SMS.
// SetCredentials replaces the Pluxee credentials the next login will use.
//
// They are held in memory only and never persisted: a restart drops them, which
// is the deliberate trade for not keeping a reusable password for a financial
// account on disk. A restored session on disk still reaches AUTHENTICATED
// without them.
//
// Refused while a challenge is outstanding — swapping credentials mid-flight
// would silently invalidate the code already in the user's hand.
func (m *Manager) SetCredentials(username, password, company string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("session: username is required")
	}
	if password == "" {
		return fmt.Errorf("session: password is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireChallengeLocked()
	if m.state == StateAwaitingOTP {
		return ErrLoginInProgress
	}

	m.username = username
	m.password = password
	m.company = strings.TrimSpace(company)
	m.log.Info("pluxee credentials set via the api")
	return nil
}

// HasCredentials reports whether a login could be attempted.
func (m *Manager) HasCredentials() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.username != "" && m.password != ""
}

func (m *Manager) Login(ctx context.Context) (*pluxee.Challenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireChallengeLocked()

	if m.state == StateAwaitingOTP {
		return nil, ErrLoginInProgress
	}
	if m.username == "" || m.password == "" {
		return nil, ErrNoCredentials
	}

	res, err := m.client.Login(ctx, m.username, m.password, m.company)
	if err != nil {
		m.resetLocked()
		return nil, err
	}

	if res.Authenticated {
		m.state = StateAuthenticated
		m.authenticatedAt = m.now()
		m.challenge = nil
		m.persistLocked()
		return nil, nil
	}

	m.state = StateAwaitingOTP
	m.challenge = res.Challenge
	m.challengeExpiry = m.now().Add(m.otpTTL)
	m.attemptsLeft = m.maxAttempts
	m.log.Info("awaiting otp",
		"method", res.Challenge.Method,
		"masked_target", res.Challenge.MaskedInput,
		"expires_at", m.challengeExpiry)
	return res.Challenge, nil
}

// SubmitOTP answers an outstanding challenge.
//
// The code is passed straight through and never stored, logged, or persisted.
func (m *Manager) SubmitOTP(ctx context.Context, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateAwaitingOTP && m.challengeExpired() {
		m.resetLocked()
		return ErrChallengeExpired
	}
	if m.state != StateAwaitingOTP || m.challenge == nil {
		return ErrNotAwaitingOTP
	}

	err := m.client.SubmitOTP(ctx, m.challenge, code)
	if err == nil {
		m.state = StateAuthenticated
		m.authenticatedAt = m.now()
		m.challenge = nil
		m.attemptsLeft = 0
		m.persistLocked()
		return nil
	}

	if !errors.Is(err, pluxee.ErrOTPRejected) {
		// Something other than a wrong code: abandon the attempt rather than
		// burning the user's remaining tries on a broken backend.
		m.resetLocked()
		return err
	}

	m.attemptsLeft--
	if m.attemptsLeft <= 0 {
		m.log.Warn("otp attempts exhausted, a fresh login is required")
		m.resetLocked()
		return &OTPRejectedError{Remaining: 0}
	}
	return &OTPRejectedError{Remaining: m.attemptsLeft}
}

// Invalidate drops the session, in memory and on disk, and returns to IDLE.
// Called when the backend rejects a supposedly good session.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.client.RestoreSession(nil)
	if err := m.store.Clear(); err != nil {
		m.log.Warn("could not remove the persisted session", "error", err)
	}
	m.resetLocked()
}

// challengeExpired reports whether the outstanding challenge has timed out.
func (m *Manager) challengeExpired() bool {
	return !m.challengeExpiry.IsZero() && m.now().After(m.challengeExpiry)
}

// expireChallengeLocked drops a challenge that has outlived its TTL.
func (m *Manager) expireChallengeLocked() {
	if m.state == StateAwaitingOTP && m.challengeExpired() {
		m.log.Info("otp challenge expired without a code")
		m.resetLocked()
	}
}

// resetLocked returns the machine to IDLE and wipes the pending challenge.
func (m *Manager) resetLocked() {
	m.state = StateIdle
	m.challenge = nil
	m.challengeExpiry = time.Time{}
	m.attemptsLeft = 0
}

// persistLocked writes the freshly established session to disk. A failure here
// is logged but not fatal: the service is authenticated either way, it will just
// need a new OTP after a restart.
func (m *Manager) persistLocked() {
	cookies := m.client.Session()
	if len(cookies) == 0 {
		m.log.Warn("authenticated but the client holds no cookies; nothing to persist")
		return
	}
	if err := m.store.Save(&Persisted{Cookies: cookies}); err != nil {
		m.log.Error("could not persist the session; a restart will need a new otp", "error", err)
		return
	}
	m.log.Info("session persisted", "path", m.store.Path())
}
