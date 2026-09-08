package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/t0mer/cubit/internal/voucher"
)

// BalanceReport is the balance plus the voucher arithmetic, as served by
// GET /api/v1/balance and printed to the console.
//
// Money appears twice: once as int64 agorot, which is the authoritative value,
// and once as a formatted string for humans.
type BalanceReport struct {
	BalanceAgorot int64  `json:"balance_agorot"`
	Balance       string `json:"balance"`
	Currency      string `json:"currency"`

	VoucherValueAgorot int64  `json:"voucher_value_agorot"`
	VoucherValue       string `json:"voucher_value"`

	VouchersAffordable int64  `json:"vouchers_affordable"`
	RemainderAgorot    int64  `json:"remainder_agorot"`
	Remainder          string `json:"remainder"`

	RestaurantID string    `json:"restaurant_id"`
	CheckedAt    time.Time `json:"checked_at"`
}

// newReport assembles a report from a voucher calculation.
func newReport(res voucher.Result, restaurantID string, checkedAt time.Time) BalanceReport {
	return BalanceReport{
		BalanceAgorot:      res.BalanceAgorot,
		Balance:            voucher.FormatAgorot(res.BalanceAgorot),
		Currency:           "ILS",
		VoucherValueAgorot: res.ValueAgorot,
		VoucherValue:       voucher.FormatAgorot(res.ValueAgorot),
		VouchersAffordable: res.Affordable,
		RemainderAgorot:    res.RemainderAgorot,
		Remainder:          voucher.FormatAgorot(res.RemainderAgorot),
		RestaurantID:       restaurantID,
		CheckedAt:          checkedAt,
	}
}

// Render returns the human-readable block printed to stdout on every successful
// balance check.
func (r BalanceReport) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cibus balance : ₪%s\n", r.Balance)
	fmt.Fprintf(&b, "Voucher value : ₪%s\n", r.VoucherValue)
	fmt.Fprintf(&b, "Affordable    : %d vouchers\n", r.VouchersAffordable)
	fmt.Fprintf(&b, "Remainder     : ₪%s\n", r.Remainder)
	return b.String()
}
