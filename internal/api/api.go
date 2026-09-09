// Package api serves Cubit's HTTP surface: the auth state machine, the balance
// report, health probes and metrics.
//
// Handlers never echo a credential, an OTP code or a token. Errors returned to
// callers describe what went wrong in terms of state, not in terms of secrets.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/t0mer/cubit/internal/apidocs"

	"github.com/t0mer/cubit/internal/metrics"
	"github.com/t0mer/cubit/internal/pluxee"
	"github.com/t0mer/cubit/internal/session"
	"github.com/t0mer/cubit/internal/voucher"
)

// maxBodyBytes caps ordinary request bodies: an OTP code, a credential pair.
const maxBodyBytes = 4 << 10

// maxSessionBytes caps the session import, which carries a whole cookie jar.
// A real Pluxee login produced 19 cookies totalling well over 4 KiB, so the
// ordinary cap rejects every genuine session.
const maxSessionBytes = 256 << 10

// Options configures a Handler.
type Options struct {
	Sessions     *session.Manager
	Client       pluxee.API
	Calculator   *voucher.Calculator
	RestaurantID string
	Metrics      *metrics.Metrics
	Logger       *slog.Logger
	Version      string
	// APIToken guards /api/v1 and /metrics. When empty the API is open — see
	// requireToken for why that is the documented first-run behaviour.
	APIToken string
	// OnReport, when set, is called with every successful balance report. The
	// service uses it to print the console block.
	OnReport func(BalanceReport)
}

// Handler serves the HTTP API.
type Handler struct {
	sessions     *session.Manager
	client       pluxee.API
	calc         *voucher.Calculator
	restaurantID string
	metrics      *metrics.Metrics
	log          *slog.Logger
	version      string
	apiToken     string
	onReport     func(BalanceReport)
}

// New returns a Handler.
func New(opts Options) (*Handler, error) {
	if opts.Sessions == nil {
		return nil, fmt.Errorf("api: a session manager is required")
	}
	if opts.Client == nil {
		return nil, fmt.Errorf("api: a pluxee client is required")
	}
	if opts.Calculator == nil {
		return nil, fmt.Errorf("api: a voucher calculator is required")
	}
	h := &Handler{
		sessions:     opts.Sessions,
		client:       opts.Client,
		calc:         opts.Calculator,
		restaurantID: opts.RestaurantID,
		metrics:      opts.Metrics,
		log:          opts.Logger,
		version:      opts.Version,
		apiToken:     opts.APIToken,
		onReport:     opts.OnReport,
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.metrics == nil {
		h.metrics = metrics.New()
	}
	return h, nil
}

// Routes builds the router.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(h.logRequests)

	// Probes stay open: an orchestrator's liveness check cannot carry a token.
	r.Get("/healthz", h.handleHealthz)
	r.Get("/readyz", h.handleReadyz)

	// The specification is documentation, not data, so it stays open too.
	apidocs.Mount(r)

	// Everything that reads the balance or drives the state machine is guarded.
	r.Group(func(r chi.Router) {
		r.Use(h.requireToken)
		r.Method(http.MethodGet, "/metrics",
			promhttp.HandlerFor(h.metrics.Registry(), promhttp.HandlerOpts{}))
		r.Route("/api/v1", func(r chi.Router) {
			r.Post("/auth/credentials", h.handleCredentials)
			r.Post("/auth/browser", h.handleBrowserLogin)
			r.Get("/auth/browser/otp", h.handleCollectOTP)
			r.Get("/auth/login-request", h.handleLoginRequest)
			r.Post("/auth/session", h.handleSessionImport)
			r.Post("/auth/logout", h.handleLogout)
			r.Post("/auth/login", h.handleLogin)
			r.Post("/auth/otp", h.handleOTP)
			r.Get("/auth/status", h.handleAuthStatus)
			r.Get("/balance", h.handleBalance)
		})
	})
	return r
}

// requireToken guards the endpoints that expose the balance or drive the login.
//
// The token may be presented as an X-API-Token header or as the password half
// of Basic Auth. Comparison is constant-time.
//
// When no token is configured the API is open. That is deliberate first-run
// behaviour — the service must be reachable before anything is configured — but
// it means an unguarded instance exposes the balance and can be made to send the
// account holder an OTP, so New logs a warning at startup and the README says so.
func (h *Handler) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.apiToken == "" {
			next.ServeHTTP(w, r)
			return
		}

		presented := r.Header.Get("X-API-Token")
		if presented == "" {
			if _, password, ok := r.BasicAuth(); ok {
				presented = password
			}
		}

		if subtle.ConstantTimeCompare([]byte(presented), []byte(h.apiToken)) != 1 {
			// Never echo either token back, and give no hint which half failed.
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "a valid X-API-Token header or basic-auth password is required",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests logs method, path and status. Bodies are never logged: the OTP
// endpoint's body is a secret.
func (h *Handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		h.log.Debug("http request",
			"method", r.Method, "path", r.URL.Path,
			"status", ww.Status(), "duration", time.Since(start))
	})
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": h.version,
	})
}

func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	state := h.sessions.State()
	h.metrics.SetAuthState(string(state))
	if state != session.StateAuthenticated {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not ready",
			"state":  state,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "state": state})
}

// authStatusBody is the shape returned by the auth endpoints.
type authStatusBody struct {
	State             session.State `json:"state"`
	MaskedTarget      string        `json:"masked_target,omitempty"`
	DeliveryMethod    string        `json:"delivery_method,omitempty"`
	AttemptsRemaining int           `json:"attempts_remaining,omitempty"`
	ExpiresAt         *time.Time    `json:"challenge_expires_at,omitempty"`
	AuthenticatedAt   *time.Time    `json:"authenticated_at,omitempty"`

	// CredentialsConfigured tells a caller whether a login can be started at
	// all, or whether credentials still need posting to /auth/credentials.
	CredentialsConfigured bool `json:"credentials_configured"`

	Message string `json:"message,omitempty"`
}

func statusBody(st session.Status, msg string) authStatusBody {
	return authStatusBody{
		State:                 st.State,
		CredentialsConfigured: st.CredentialsConfigured,
		MaskedTarget:          st.MaskedTarget,
		DeliveryMethod:        st.DeliveryMethod,
		AttemptsRemaining:     st.AttemptsRemaining,
		ExpiresAt:             st.ChallengeExpiresAt,
		AuthenticatedAt:       st.AuthenticatedAt,
		Message:               msg,
	}
}

func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	st := h.sessions.Status()
	h.metrics.SetAuthState(string(st.State))
	writeJSON(w, http.StatusOK, statusBody(st, ""))
}

// handleLogin starts a login. A successful start returns 202 with the masked
// destination the OTP was sent to.
// credentialsRequest is the body of POST /api/v1/auth/credentials.
type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Company  string `json:"company"`
}

// handleCredentials accepts Pluxee credentials at runtime, so they need not be
// baked into the configuration.
//
// It refuses to run on an unguarded instance. Every other endpoint is open when
// no API token is configured, which is deliberate first-run behaviour, but an
// open endpoint that accepts a password for a financial account is a different
// proposition: anyone reachable on the network could feed cubit their own
// credentials, and yours would cross the wire unprotected.
//
// The credentials are held in memory only, and nothing here is ever logged or
// echoed back.
func (h *Handler) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if h.apiToken == "" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{
			"error": "configure an api token (server.api_token) before posting credentials",
		})
		return
	}

	var req credentialsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	// Deliberately not echoing the body: it holds the password.
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "body must be json of the form {\"username\":\"...\",\"password\":\"...\"}",
		})
		return
	}

	switch err := h.sessions.SetCredentials(req.Username, req.Password, req.Company); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, session.ErrLoginInProgress):
		writeJSON(w, http.StatusConflict, statusBody(h.sessions.Status(),
			"a login is awaiting an otp; answer or let it expire before changing credentials"))
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "username and password are both required",
		})
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	ch, err := h.sessions.Login(r.Context())
	if err != nil {
		h.metrics.RecordLogin("error")
		h.writeAuthError(w, err)
		return
	}

	st := h.sessions.Status()
	h.metrics.SetAuthState(string(st.State))
	h.metrics.RecordLogin("success")

	if ch == nil {
		writeJSON(w, http.StatusOK, statusBody(st, "already authenticated, no otp required"))
		return
	}
	writeJSON(w, http.StatusAccepted, statusBody(st, "an otp has been sent; post it to /api/v1/auth/otp"))
}

// otpRequest is the body of POST /api/v1/auth/otp.
type otpRequest struct {
	Code string `json:"code"`
}

func (h *Handler) handleOTP(w http.ResponseWriter, r *http.Request) {
	var req otpRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// Deliberately not echoing the body: it holds the code.
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "request body must be a JSON object of the form {\"code\":\"123456\"}",
		})
		return
	}
	if req.Code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code is required"})
		return
	}

	err := h.sessions.SubmitOTP(r.Context(), req.Code)
	st := h.sessions.Status()
	h.metrics.SetAuthState(string(st.State))

	if err != nil {
		h.metrics.RecordOTPSubmission("rejected")
		h.writeAuthError(w, err)
		return
	}

	h.metrics.RecordOTPSubmission("accepted")

	// In a browser-assisted login the code is parked for the helper rather than
	// submitted, so the machine is still awaiting: say so instead of claiming an
	// authentication that has not happened yet.
	if st.State == session.StateAwaitingOTP {
		writeJSON(w, http.StatusAccepted, statusBody(st,
			"code received; the browser-assisted login will complete it"))
		return
	}
	writeJSON(w, http.StatusOK, statusBody(st, "authenticated"))
}

// writeAuthError maps a session or client error onto the documented status code.
func (h *Handler) writeAuthError(w http.ResponseWriter, err error) {
	st := h.sessions.Status()

	var otpErr *session.OTPRejectedError
	switch {
	case errors.As(err, &otpErr):
		body := statusBody(st, "incorrect code")
		body.AttemptsRemaining = otpErr.Remaining
		writeJSON(w, http.StatusUnauthorized, body)

	case errors.Is(err, session.ErrAlreadyAuthenticated):
		writeJSON(w, http.StatusConflict, statusBody(st,
			"already authenticated; there is no need to log in again"))

	case errors.Is(err, session.ErrChallengeExpired):
		writeJSON(w, http.StatusGone, statusBody(st, "the otp challenge expired; start a new login"))

	case errors.Is(err, session.ErrLoginInProgress):
		writeJSON(w, http.StatusConflict, statusBody(st, "a login is already awaiting an otp"))

	case errors.Is(err, session.ErrNoCredentials):
		writeJSON(w, http.StatusPreconditionFailed, statusBody(st,
			"no pluxee credentials are configured; post them to /api/v1/auth/credentials"))

	case errors.Is(err, session.ErrNotAwaitingOTP):
		writeJSON(w, http.StatusConflict, statusBody(st, "no otp is pending; start a login first"))

	case errors.Is(err, pluxee.ErrInvalidCredentials):
		writeJSON(w, http.StatusUnauthorized, statusBody(st, "pluxee rejected the configured credentials"))

	case errors.Is(err, pluxee.ErrCaptchaRequired):
		writeJSON(w, http.StatusPreconditionFailed, statusBody(st,
			"pluxee requires a reCAPTCHA token to log in, and it cannot be minted "+
				"outside a browser; see the re-authentication section of the README"))

	case errors.Is(err, pluxee.ErrIncompleteChallenge):
		writeJSON(w, http.StatusBadGateway, statusBody(st,
			"pluxee signalled an otp challenge but withheld its handle; the login cannot continue"))

	case errors.Is(err, pluxee.ErrNoDeliveryTarget):
		writeJSON(w, http.StatusPreconditionFailed, statusBody(st,
			"the account has no phone number on file, so no otp can be delivered"))

	default:
		h.log.Error("authentication failed", "error", err)
		writeJSON(w, http.StatusBadGateway, statusBody(st, "the pluxee backend could not be reached"))
	}
}

// handleBalance fetches the balance and computes the voucher count.
func (h *Handler) handleBalance(w http.ResponseWriter, r *http.Request) {
	if state := h.sessions.State(); state != session.StateAuthenticated {
		h.metrics.SetAuthState(string(state))
		writeJSON(w, http.StatusServiceUnavailable, statusBody(h.sessions.Status(),
			"not authenticated; start a login and submit the otp"))
		return
	}

	report, err := h.Check(r.Context())
	if err != nil {
		if errors.Is(err, pluxee.ErrSessionExpired) {
			writeJSON(w, http.StatusServiceUnavailable, statusBody(h.sessions.Status(),
				"the pluxee session expired; start a new login"))
			return
		}
		h.log.Error("balance check failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "could not fetch the balance from pluxee",
		})
		return
	}

	writeJSON(w, http.StatusOK, report)
}

// Check fetches the balance, computes the voucher count and reports it.
//
// It is exported because the service also calls it outside an HTTP request, on
// startup, to print the balance block to the console.
func (h *Handler) Check(ctx context.Context) (BalanceReport, error) {
	balanceAgorot, err := h.client.Balance(ctx)
	if err != nil {
		h.metrics.RecordBalanceCheck("error")
		if errors.Is(err, pluxee.ErrSessionExpired) {
			// The session we thought was good is not: drop it so the next
			// caller is told to log in rather than being handed a stale error.
			h.sessions.Invalidate()
			h.metrics.SetAuthState(string(session.StateIdle))
		}
		return BalanceReport{}, err
	}

	now := time.Now()
	report := newReport(h.calc.Calculate(balanceAgorot), h.restaurantID, now)

	h.metrics.RecordBalanceCheck("success")
	h.metrics.RecordBalance(report.BalanceAgorot, report.VouchersAffordable)
	h.metrics.RecordRemainder(report.RemainderAgorot)
	h.metrics.RecordCheckTime(now.Unix())

	h.log.Info("balance checked",
		"balance_agorot", report.BalanceAgorot,
		"voucher_value_agorot", report.VoucherValueAgorot,
		"vouchers_affordable", report.VouchersAffordable,
		"remainder_agorot", report.RemainderAgorot,
		"restaurant_id", report.RestaurantID)

	if h.onReport != nil {
		h.onReport(report)
	}
	return report, nil
}

// writeJSON writes v as JSON with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status is already written, so there is nothing to do but note it.
		slog.Default().Error("writing json response", "error", err)
	}
}
