package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/t0mer/cubit/internal/session"
)

// A browser-assisted login is driven by the cubit-login helper. Pluxee enforces
// reCAPTCHA on both the credential post and the OTP submission, and only a real
// browser can mint a token, so the helper owns the exchange with Pluxee and
// cubit plays two small parts:
//
//  1. it catches the code the user's phone posts to /api/v1/auth/otp and hands
//     it to the waiting helper, once;
//  2. it adopts the session the helper captured, if that session works.
//
// Both endpoints refuse to run on an unguarded instance, for the same reason
// /auth/credentials does: a session cookie is as good as the password.

type browserLoginRequest struct {
	MaskedTarget string `json:"masked_target"`
	Method       string `json:"method"`
}

type sessionCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
}

type sessionImportRequest struct {
	Cookies []sessionCookie `json:"cookies"`
}

// guardedForHandoff reports whether the instance is protected well enough to
// accept a browser handoff, writing the refusal if it is not.
func (h *Handler) guardedForHandoff(w http.ResponseWriter) bool {
	if h.apiToken != "" {
		return true
	}
	writeJSON(w, http.StatusPreconditionFailed, map[string]string{
		"error": "configure an api token (server.api_token) before using browser-assisted login",
	})
	return false
}

// handleBrowserLogin arms cubit to receive an OTP for a login the helper is
// driving. It never touches the Pluxee backend: there is no challenge here to
// submit against.
func (h *Handler) handleBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if !h.guardedForHandoff(w) {
		return
	}

	var req browserLoginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": `body must be json of the form {"masked_target":"...","method":"..."}`,
		})
		return
	}

	if err := h.sessions.ExpectExternalOTP(req.MaskedTarget, req.Method); err != nil {
		h.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, statusBody(h.sessions.Status(),
		"waiting for the otp; it may be posted to /api/v1/auth/otp"))
}

// handleCollectOTP hands the parked code to the helper, exactly once.
func (h *Handler) handleCollectOTP(w http.ResponseWriter, _ *http.Request) {
	if !h.guardedForHandoff(w) {
		return
	}

	code, ok := h.sessions.CollectExternalOTP()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

// handleSessionImport adopts a session captured by the helper.
func (h *Handler) handleSessionImport(w http.ResponseWriter, r *http.Request) {
	if !h.guardedForHandoff(w) {
		return
	}

	var req sessionImportRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSessionBytes))
	dec.DisallowUnknownFields()
	// Deliberately not echoing the body: it holds a live session. The reason is
	// logged instead, so "too large" and "malformed" are tellable apart without
	// putting the jar anywhere.
	if err := dec.Decode(&req); err != nil {
		h.log.Debug("rejected a session import", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "body must be json carrying a non-empty cookies array",
		})
		return
	}
	if len(req.Cookies) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "body must be json carrying a non-empty cookies array",
		})
		return
	}

	cookies := make([]*http.Cookie, 0, len(req.Cookies))
	for _, c := range req.Cookies {
		if c.Name == "" {
			continue
		}
		cookie := &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HttpOnly: c.HTTPOnly,
		}
		if c.Expires > 0 {
			cookie.Expires = time.Unix(int64(c.Expires), 0)
		}
		cookies = append(cookies, cookie)
	}
	if len(cookies) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no usable cookies in the request",
		})
		return
	}

	switch err := h.sessions.AdoptSession(r.Context(), cookies); {
	case err == nil:
		h.log.Info("adopted a session from the cubit-login helper")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, session.ErrSessionNotUsable):
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "the supplied session is not accepted by pluxee",
		})
	default:
		h.log.Error("could not adopt the supplied session", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "could not reach pluxee to validate the supplied session",
		})
	}
}

// handleLogout ends the session, at Pluxee first and then here.
//
// Idempotent: logging out when nothing is held is a success, not an error, so a
// caller can always reach a known state without checking first.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := h.sessions.Logout(r.Context()); err != nil {
		h.log.Error("logout failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "could not complete the logout",
		})
		return
	}
	h.metrics.SetAuthState(string(h.sessions.State()))
	w.WriteHeader(http.StatusNoContent)
}

// handleLoginRequest hands a pending browser-login request to a waiting helper.
//
// This returns the Pluxee password, which nothing else in the API ever does.
// That is the price of letting a POST to /auth/credentials trigger a real
// login: the helper drives the actual login form and cannot fill it without
// them. It is guarded by the API token, refuses to run unguarded, and is
// single-use, so the credentials cross the wire once per login rather than on
// every poll.
func (h *Handler) handleLoginRequest(w http.ResponseWriter, _ *http.Request) {
	if !h.guardedForHandoff(w) {
		return
	}

	req, ok := h.sessions.TakeLoginRequest()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.log.Info("handing a browser-login request to a helper")
	writeJSON(w, http.StatusOK, map[string]string{
		"username": req.Username,
		"password": req.Password,
		"company":  req.Company,
	})
}
