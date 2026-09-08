package voucher

import "testing"

func TestNewRejectsNonPositiveDenomination(t *testing.T) {
	for _, value := range []int64{0, -1, -5000} {
		if _, err := New(value); err == nil {
			t.Errorf("New(%d): want error, got nil", value)
		}
	}
}

func TestNewAcceptsPositiveDenomination(t *testing.T) {
	if _, err := New(5000); err != nil {
		t.Fatalf("New(5000): unexpected error: %v", err)
	}
}

func TestCalculate(t *testing.T) {
	tests := []struct {
		name          string
		value         int64
		balance       int64
		wantVouchers  int64
		wantRemainder int64
	}{
		{"zero balance", 5000, 0, 0, 0},
		{"below one voucher", 5000, 4999, 0, 4999},
		{"exactly one voucher", 5000, 5000, 1, 0},
		{"one agora short of two", 5000, 9999, 1, 4999},
		{"exact multiple", 5000, 25000, 5, 0},
		{"spec example 273.50", 5000, 27350, 5, 2350},
		{"huge balance", 5000, 999999999999, 199999999, 4999},
		{"denomination of one agora", 1, 12345, 12345, 0},
		{"negative smaller than denomination", 5000, -100, 0, -100},
		{"negative larger than denomination", 5000, -6000, 0, -6000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.value)
			if err != nil {
				t.Fatalf("New(%d): %v", tc.value, err)
			}
			got := c.Calculate(tc.balance)
			if got.Affordable != tc.wantVouchers {
				t.Errorf("Affordable = %d, want %d", got.Affordable, tc.wantVouchers)
			}
			if got.RemainderAgorot != tc.wantRemainder {
				t.Errorf("RemainderAgorot = %d, want %d", got.RemainderAgorot, tc.wantRemainder)
			}
			if got.BalanceAgorot != tc.balance {
				t.Errorf("BalanceAgorot = %d, want %d", got.BalanceAgorot, tc.balance)
			}
			if got.ValueAgorot != tc.value {
				t.Errorf("ValueAgorot = %d, want %d", got.ValueAgorot, tc.value)
			}
		})
	}
}

func TestCalculateNeverOverspends(t *testing.T) {
	c, err := New(5000)
	if err != nil {
		t.Fatal(err)
	}
	for balance := int64(0); balance < 20000; balance += 137 {
		got := c.Calculate(balance)
		if spent := got.Affordable * got.ValueAgorot; spent > balance {
			t.Fatalf("balance %d: vouchers cost %d, more than the balance", balance, spent)
		}
		if got.Affordable*got.ValueAgorot+got.RemainderAgorot != balance {
			t.Fatalf("balance %d: vouchers+remainder does not reconstruct the balance", balance)
		}
	}
}

func TestFormatAgorot(t *testing.T) {
	tests := []struct {
		agorot int64
		want   string
	}{
		{0, "0.00"},
		{5, "0.05"},
		{50, "0.50"},
		{100, "1.00"},
		{27350, "273.50"},
		{5000, "50.00"},
		{-2350, "-23.50"},
		{-5, "-0.05"},
	}
	for _, tc := range tests {
		if got := FormatAgorot(tc.agorot); got != tc.want {
			t.Errorf("FormatAgorot(%d) = %q, want %q", tc.agorot, got, tc.want)
		}
	}
}
