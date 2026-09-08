// Package voucher computes how many fixed-denomination vouchers a balance can
// buy. It is pure arithmetic: no I/O, no network, no clock.
//
// All money is int64 agorot. Never float64 — a balance of 273.50 shekels is
// 27350 agorot, and it stays an integer from the moment it crosses the API
// boundary until it is formatted for display.
package voucher

import (
	"fmt"
)

// Calculator computes voucher counts against a fixed denomination.
//
// The denomination is validated once, at construction, so that Calculate cannot
// fail and cannot divide by zero at runtime.
type Calculator struct {
	valueAgorot int64
}

// Result is the outcome of a single voucher calculation.
type Result struct {
	// BalanceAgorot is the balance the calculation was made against.
	BalanceAgorot int64
	// ValueAgorot is the denomination of a single voucher.
	ValueAgorot int64
	// Affordable is how many whole vouchers the balance covers. Never negative.
	Affordable int64
	// RemainderAgorot is what is left over after buying Affordable vouchers.
	// For a negative balance this is the balance itself.
	RemainderAgorot int64
}

// New returns a Calculator for the given voucher denomination in agorot.
//
// It returns an error if valueAgorot is not positive; callers are expected to
// treat that as a configuration error at startup rather than handling it later.
func New(valueAgorot int64) (*Calculator, error) {
	if valueAgorot <= 0 {
		return nil, fmt.Errorf("voucher value must be positive, got %d agorot", valueAgorot)
	}
	return &Calculator{valueAgorot: valueAgorot}, nil
}

// ValueAgorot returns the configured voucher denomination.
func (c *Calculator) ValueAgorot() int64 { return c.valueAgorot }

// Calculate returns how many vouchers balanceAgorot covers, and the remainder.
//
// A zero or negative balance buys nothing: Affordable is 0 and the whole balance
// is the remainder. Go's division truncates toward zero, which would otherwise
// report -1 vouchers for a balance of -6000 against a 5000 denomination.
func (c *Calculator) Calculate(balanceAgorot int64) Result {
	res := Result{
		BalanceAgorot: balanceAgorot,
		ValueAgorot:   c.valueAgorot,
	}
	if balanceAgorot <= 0 {
		res.RemainderAgorot = balanceAgorot
		return res
	}
	res.Affordable = balanceAgorot / c.valueAgorot
	res.RemainderAgorot = balanceAgorot % c.valueAgorot
	return res
}

// FormatAgorot renders agorot as a plain decimal string with two places, e.g.
// 27350 becomes "273.50". No currency symbol: callers add their own.
func FormatAgorot(agorot int64) string {
	sign := ""
	if agorot < 0 {
		sign = "-"
		agorot = -agorot
	}
	return fmt.Sprintf("%s%d.%02d", sign, agorot/100, agorot%100)
}
