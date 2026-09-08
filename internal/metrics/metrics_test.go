package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordBalanceExposesGauges(t *testing.T) {
	m := New()
	m.RecordBalance(27350, 5)

	if got := testutil.ToFloat64(m.balanceAgorot); got != 27350 {
		t.Errorf("balance gauge = %v, want 27350", got)
	}
	if got := testutil.ToFloat64(m.vouchersAffordable); got != 5 {
		t.Errorf("vouchers gauge = %v, want 5", got)
	}
}

func TestRecordBalanceCheckCountsOutcomes(t *testing.T) {
	m := New()
	m.RecordBalanceCheck("success")
	m.RecordBalanceCheck("success")
	m.RecordBalanceCheck("error")

	if got := testutil.ToFloat64(m.balanceChecks.WithLabelValues("success")); got != 2 {
		t.Errorf("success checks = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.balanceChecks.WithLabelValues("error")); got != 1 {
		t.Errorf("error checks = %v, want 1", got)
	}
}

func TestSetAuthStateIsExclusive(t *testing.T) {
	m := New()
	m.SetAuthState("AWAITING_OTP")
	m.SetAuthState("AUTHENTICATED")

	if got := testutil.ToFloat64(m.authState.WithLabelValues("AUTHENTICATED")); got != 1 {
		t.Errorf("AUTHENTICATED = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.authState.WithLabelValues("AWAITING_OTP")); got != 0 {
		t.Errorf("AWAITING_OTP = %v, want 0 once the state moved on", got)
	}
}

func TestRegistryExposesCubitMetrics(t *testing.T) {
	m := New()
	m.RecordBalance(100, 0)

	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}
	joined := strings.Join(names, " ")
	for _, want := range []string{"cubit_balance_agorot", "cubit_vouchers_affordable"} {
		if !strings.Contains(joined, want) {
			t.Errorf("registry does not expose %s (has: %s)", want, joined)
		}
	}
}
