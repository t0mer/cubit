package pluxee

import (
	"context"
	"fmt"
	"net/http"
)

// Login starts a password login.
//
// The backend answers in one of three ways (docs/api-notes.md §4.1):
//   - 200: already authenticated, a session cookie is now held;
//   - 210: an OTP has been sent and a Challenge is returned;
//   - 401: the credentials were rejected.
//
// A rejected login is never retried.
func (c *Client) Login(ctx context.Context, username, password, company string) (*LoginResult, error) {
	raw, status, err := c.postJSON(ctx, c.authBase+"/auth/authToken", authRequest{
		Username:       username,
		Password:       password,
		Company:        company,
		RecaptchaToken: c.recaptchaToken,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errEmptyResponse
	}

	res, err := decodeAuth(raw, status)
	if err != nil {
		return nil, err
	}

	switch {
	case res.Status == 210:
		// The account exists and an OTP was dispatched. Without a masked target
		// there is nowhere for the code to go, so the flow cannot continue.
		if res.Data.MaskedInput == "" {
			return nil, ErrNoDeliveryTarget
		}
		ch := &Challenge{
			UserInput1:  res.Data.UserInput1,
			UserInput2:  res.Data.UserInput2,
			MaskedInput: res.Data.MaskedInput,
			Method:      res.Data.Method,
		}
		// The backend does not always echo the identifiers back; falling back to
		// what we sent keeps the OTP submission well-formed.
		if ch.UserInput1 == "" {
			ch.UserInput1 = username
		}
		if ch.UserInput2 == "" {
			ch.UserInput2 = company
		}
		c.log.Info("pluxee requires an otp",
			"method", ch.Method, "masked_target", ch.MaskedInput)
		return &LoginResult{Challenge: ch}, nil

	case res.Status == http.StatusOK || res.Status == http.StatusCreated:
		c.log.Info("pluxee authenticated without an otp challenge")
		return &LoginResult{Authenticated: true}, nil

	case res.isCaptcha():
		return nil, ErrCaptchaRequired

	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden:
		return nil, ErrInvalidCredentials

	default:
		return nil, errUnexpected(res)
	}
}

// SubmitOTP completes a login by submitting the code the user received.
//
// It posts to the same endpoint as Login but with the OTP shape, replaying the
// identifiers the backend handed back with the challenge. A rejected code is
// never retried; the caller counts attempts and gives up.
func (c *Client) SubmitOTP(ctx context.Context, ch *Challenge, code string) error {
	if ch == nil {
		return fmt.Errorf("pluxee: no pending challenge")
	}
	if code == "" {
		return fmt.Errorf("pluxee: empty otp code")
	}

	raw, status, err := c.postJSON(ctx, c.authBase+"/auth/authToken", authRequest{
		OTPPin:         code,
		UserInput1:     ch.UserInput1,
		UserInput2:     ch.UserInput2,
		TrustDevice:    false,
		RecaptchaToken: c.recaptchaToken,
	})
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errEmptyResponse
	}

	res, err := decodeAuth(raw, status)
	if err != nil {
		return err
	}

	switch {
	case res.Status == http.StatusOK || res.Status == http.StatusCreated:
		c.log.Info("pluxee login completed")
		return nil
	case res.isCaptcha():
		return ErrCaptchaRequired
	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden ||
		res.Status == http.StatusBadRequest:
		return ErrOTPRejected
	default:
		return errUnexpected(res)
	}
}
