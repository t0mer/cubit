package session

// LoginRequest is a standing ask for a helper to perform a browser-assisted
// login. It carries the credentials because the helper needs them to fill the
// real login form.
type LoginRequest struct {
	Username string
	Password string
	Company  string
}

// TakeLoginRequest hands a pending request to a waiting helper, exactly once.
//
// Single-use for the same reason the OTP relay is: this carries a password, and
// a poll loop that could re-read it would keep re-exposing it. Once taken, the
// helper owns the attempt.
func (m *Manager) TakeLoginRequest() (LoginRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.loginRequested || m.username == "" || m.password == "" {
		return LoginRequest{}, false
	}
	m.loginRequested = false
	return LoginRequest{
		Username: m.username,
		Password: m.password,
		Company:  m.company,
	}, true
}
