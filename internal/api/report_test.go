package api

import (
	"strings"
	"testing"
	"time"

	"github.com/t0mer/cubit/internal/voucher"
)

func TestRenderProducesTheContractedBlock(t *testing.T) {
	calc, err := voucher.New(5000)
	if err != nil {
		t.Fatal(err)
	}
	r := newReport(calc.Calculate(27350), "31999", time.Now())

	want := "Cibus balance : ₪273.50\n" +
		"Voucher value : ₪50.00\n" +
		"Affordable    : 5 vouchers\n" +
		"Remainder     : ₪23.50\n"

	if got := r.Render(); got != want {
		t.Errorf("Render() =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderHandlesAZeroBalance(t *testing.T) {
	calc, _ := voucher.New(5000)
	got := newReport(calc.Calculate(0), "31999", time.Now()).Render()

	if !strings.Contains(got, "Affordable    : 0 vouchers") {
		t.Errorf("Render() = %q, want 0 vouchers", got)
	}
	if !strings.Contains(got, "Cibus balance : ₪0.00") {
		t.Errorf("Render() = %q, want a zero balance line", got)
	}
}
