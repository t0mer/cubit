package pluxee

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseAgorot converts a decimal money string from the Pluxee API into int64
// agorot.
//
// The parsing is done on the digits, not via float64: "1.15" is exactly 115
// agorot, whereas 1.15 * 100 in binary floating point is 114.99999999999999.
// This is the single conversion point between the API's representation and the
// integer arithmetic used everywhere else.
//
// Input with more precision than an agora is rounded half away from zero.
func ParseAgorot(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty money value")
	}

	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}

	whole, frac, hasFrac := strings.Cut(s, ".")
	if strings.ContainsAny(whole, ".") || strings.Contains(frac, ".") {
		return 0, fmt.Errorf("malformed money value %q", s)
	}
	if whole == "" && frac == "" {
		return 0, fmt.Errorf("malformed money value %q", s)
	}

	var agorot int64
	if whole != "" {
		if !isDigits(whole) {
			return 0, fmt.Errorf("malformed money value %q", s)
		}
		shekels, err := strconv.ParseInt(whole, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing money value %q: %w", s, err)
		}
		agorot = shekels * 100
	}

	if hasFrac {
		if frac == "" || !isDigits(frac) {
			return 0, fmt.Errorf("malformed money value %q", s)
		}
		// Pad to at least three digits so digit three can drive the rounding.
		for len(frac) < 3 {
			frac += "0"
		}
		hundredths, err := strconv.ParseInt(frac[:2], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing money value %q: %w", s, err)
		}
		agorot += hundredths
		if frac[2] >= '5' {
			agorot++
		}
	}

	if neg {
		agorot = -agorot
	}
	return agorot, nil
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
