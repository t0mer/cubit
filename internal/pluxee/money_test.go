package pluxee

import "testing"

func TestParseAgorot(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"273.50", 27350},
		{"273.5", 27350},
		{"273", 27300},
		{"0.05", 5},
		{"0.5", 50},
		{".5", 50},
		{"-23.50", -2350},
		{"-0.05", -5},
		{"1234567.89", 123456789},
		{" 273.50 ", 27350},
		{"+273.50", 27350},
		// More precision than an agora: round half away from zero.
		{"273.504", 27350},
		{"273.505", 27351},
		{"273.999", 27400},
		{"-273.505", -27351},
		// Floats that would be lossy via float64 arithmetic.
		{"1.15", 115},
		{"8.20", 820},
	}
	for _, tc := range tests {
		got, err := ParseAgorot(tc.in)
		if err != nil {
			t.Errorf("ParseAgorot(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseAgorot(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseAgorotRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "  ", "abc", "27.3.5", "1e5", "--1", "12,50"} {
		if got, err := ParseAgorot(in); err == nil {
			t.Errorf("ParseAgorot(%q) = %d, want error", in, got)
		}
	}
}
