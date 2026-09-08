package pluxee

import (
	"context"
	"encoding/json"
	"fmt"
)

// Balance returns the account's current credit in agorot.
//
// The figure is the sum of CurrBudget across every element of prx_get_budgets,
// which is exactly what the Pluxee web app displays as the user's credit
// (docs/api-notes.md §6).
//
// The legacy backend answers HTTP 200 even for failures, so the response code in
// the body is the real status.
func (c *Client) Balance(ctx context.Context) (int64, error) {
	raw, status, err := c.postJSON(ctx, c.apiBase, map[string]string{"type": "prx_get_budgets"})
	if err != nil {
		return 0, err
	}
	if len(raw) == 0 {
		return 0, errEmptyResponse
	}

	var res legacyResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("decoding budgets response (http %d): %w", status, err)
	}

	if res.Code != 0 {
		if isAuthorizationCode(res.Code) {
			return 0, fmt.Errorf("%w: %s (code %d)", ErrSessionExpired, res.Msg, res.Code)
		}
		return 0, fmt.Errorf("pluxee: prx_get_budgets failed: %s (code %d)", res.Msg, res.Code)
	}

	var total int64
	for i, b := range res.Data {
		if b.CurrBudget == "" {
			continue
		}
		agorot, err := ParseAgorot(b.CurrBudget.String())
		if err != nil {
			return 0, fmt.Errorf("parsing CurrBudget of budget %d: %w", i, err)
		}
		total += agorot
	}
	return total, nil
}
