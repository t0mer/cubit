// Package metrics holds Cubit's Prometheus instrumentation.
//
// Everything lives on a private registry rather than the global default, so a
// test can build an isolated set of metrics and the exposed surface is exactly
// what this package declares.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Known authentication states, so every series exists from the first scrape
// rather than appearing only once a state has been entered.
var authStates = []string{"IDLE", "AWAITING_OTP", "AUTHENTICATED"}

// Metrics is Cubit's instrumentation.
type Metrics struct {
	registry *prometheus.Registry

	balanceAgorot      prometheus.Gauge
	vouchersAffordable prometheus.Gauge
	remainderAgorot    prometheus.Gauge
	lastCheck          prometheus.Gauge
	balanceChecks      *prometheus.CounterVec
	authState          *prometheus.GaugeVec
	otpSubmissions     *prometheus.CounterVec
	logins             *prometheus.CounterVec
}

// New builds the metric set on a fresh registry.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),

		balanceAgorot: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cubit_balance_agorot",
			Help: "Most recently observed Cibus balance, in agorot.",
		}),
		vouchersAffordable: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cubit_vouchers_affordable",
			Help: "Number of whole vouchers the most recent balance covers.",
		}),
		remainderAgorot: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cubit_remainder_agorot",
			Help: "Balance left over after buying the affordable vouchers, in agorot.",
		}),
		lastCheck: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "cubit_last_balance_check_timestamp_seconds",
			Help: "Unix timestamp of the last successful balance check.",
		}),
		balanceChecks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cubit_balance_checks_total",
			Help: "Balance checks by outcome.",
		}, []string{"result"}),
		authState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cubit_auth_state",
			Help: "Current authentication state; exactly one label is 1.",
		}, []string{"state"}),
		otpSubmissions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cubit_otp_submissions_total",
			Help: "OTP submissions by outcome.",
		}, []string{"result"}),
		logins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cubit_logins_total",
			Help: "Login attempts by outcome.",
		}, []string{"result"}),
	}

	m.registry.MustRegister(
		m.balanceAgorot, m.vouchersAffordable, m.remainderAgorot, m.lastCheck,
		m.balanceChecks, m.authState, m.otpSubmissions, m.logins,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	for _, s := range authStates {
		m.authState.WithLabelValues(s).Set(0)
	}
	m.authState.WithLabelValues("IDLE").Set(1)

	return m
}

// Registry returns the registry to expose on /metrics.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// RecordBalance publishes the outcome of a balance check.
func (m *Metrics) RecordBalance(balanceAgorot, vouchers int64) {
	m.balanceAgorot.Set(float64(balanceAgorot))
	m.vouchersAffordable.Set(float64(vouchers))
}

// RecordRemainder publishes the leftover balance.
func (m *Metrics) RecordRemainder(agorot int64) {
	m.remainderAgorot.Set(float64(agorot))
}

// RecordCheckTime stamps the time of a successful check.
func (m *Metrics) RecordCheckTime(unixSeconds int64) {
	m.lastCheck.Set(float64(unixSeconds))
}

// RecordBalanceCheck counts a balance check outcome.
func (m *Metrics) RecordBalanceCheck(result string) {
	m.balanceChecks.WithLabelValues(result).Inc()
}

// RecordOTPSubmission counts an OTP submission outcome.
func (m *Metrics) RecordOTPSubmission(result string) {
	m.otpSubmissions.WithLabelValues(result).Inc()
}

// RecordLogin counts a login attempt outcome.
func (m *Metrics) RecordLogin(result string) {
	m.logins.WithLabelValues(result).Inc()
}

// SetAuthState marks state as current and clears every other state, so the
// series always sums to one.
func (m *Metrics) SetAuthState(state string) {
	for _, s := range authStates {
		if s == state {
			m.authState.WithLabelValues(s).Set(1)
			continue
		}
		m.authState.WithLabelValues(s).Set(0)
	}
}
