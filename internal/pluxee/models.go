package pluxee

import (
	"errors"
	"strings"
)

// Sentinel errors. Callers branch on these rather than on status codes, so the
// mapping from the two very different backend error conventions lives here.
var (
	// ErrInvalidCredentials is a rejected username/password. Never retried.
	ErrInvalidCredentials = errors.New("pluxee: invalid credentials")
	// ErrOTPRejected is a wrong or stale OTP code. Never retried.
	ErrOTPRejected = errors.New("pluxee: otp rejected")
	// ErrCaptchaRequired means the backend demanded a reCAPTCHA token we cannot
	// produce unattended. See docs/api-notes.md §5.
	ErrCaptchaRequired = errors.New("pluxee: recaptcha token required")
	// ErrSessionExpired means the session cookie is missing or no longer valid.
	ErrSessionExpired = errors.New("pluxee: session expired")
	// ErrNoDeliveryTarget means the account has no phone number on file, so no
	// OTP can be delivered and login cannot proceed.
	ErrNoDeliveryTarget = errors.New("pluxee: account has no otp delivery target")
)

// Challenge is a pending OTP challenge returned by a password login.
//
// UserInput1 and UserInput2 are opaque identifiers the backend echoes back; they
// must be replayed verbatim when the code is submitted.
type Challenge struct {
	UserInput1  string `json:"user_input_1"`
	UserInput2  string `json:"user_input_2"`
	MaskedInput string `json:"masked_input"`
	Method      string `json:"method"`
}

// LoginResult is the outcome of a password login: either the session is already
// established, or an OTP challenge is pending.
type LoginResult struct {
	Authenticated bool
	Challenge     *Challenge
}

// authRequest is the /auth/authToken body. The endpoint takes two different
// shapes; omitempty keeps the unused half off the wire.
type authRequest struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Company  string `json:"company,omitempty"`

	OTPPin      string `json:"otpPin,omitempty"`
	UserInput1  string `json:"userInput1,omitempty"`
	UserInput2  string `json:"userInput2,omitempty"`
	TrustDevice bool   `json:"trustDevice,omitempty"`

	RecaptchaToken string `json:"reCAPTCHAToken,omitempty"`
}

// authResponse is the CAPIR envelope. The meaningful status may arrive in this
// body rather than on the HTTP status line, so both are consulted.
type authResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Data struct {
		MaskedInput string `json:"maskedInput"`
		Method      string `json:"method"`
		UserInput1  string `json:"userInput1"`
		UserInput2  string `json:"userInput2"`
	} `json:"data"`
}

// isCaptcha reports whether the backend rejected the call for a missing or bad
// reCAPTCHA token, as opposed to bad credentials.
func (r *authResponse) isCaptcha() bool {
	return strings.Contains(strings.ToLower(r.Error.Message), "recaptcha")
}

// legacyResponse is the main.py envelope. It always arrives on HTTP 200; Code is
// the real status and zero means success.
type legacyResponse struct {
	Code     int             `json:"code"`
	Msg      string          `json:"msg"`
	HTTPCode int             `json:"http_code"`
	Data     []budgetElement `json:"data"`
}

// budgetElement is one entry of prx_get_budgets. Only CurrBudget is load-bearing
// for stage 1.
type budgetElement struct {
	CurrBudget   rawNumber `json:"CurrBudget"`
	CreationDate string    `json:"CreationDate"`
}

// rawNumber preserves a JSON value's literal text whether it arrived quoted or
// bare. Decoding money into float64 and multiplying by 100 loses agorot; keeping
// the digits lets ParseAgorot do exact integer conversion.
type rawNumber string

func (n *rawNumber) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" {
		*n = ""
		return nil
	}
	*n = rawNumber(strings.Trim(s, `"`))
	return nil
}

func (n rawNumber) String() string { return string(n) }

// isAuthorizationCode reports whether a legacy response code means the session
// is gone. The SPA treats 172-179 this way; 177 is "Can't find cookie token".
func isAuthorizationCode(code int) bool {
	return code >= 172 && code <= 179
}
