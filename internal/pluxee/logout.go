package pluxee

import (
	"context"
	"encoding/json"
	"fmt"
)

// Logout ends the session at Pluxee.
//
// This is the call the consumer web app makes when a user signs out. Without
// it the cookies remain valid upstream until they expire on their own, so a
// copy of the persisted session would keep working long after cubit "logged
// out". Verified live: the endpoint answers {"code":0,"msg":"OK"}.
func (c *Client) Logout(ctx context.Context) error {
	raw, status, err := c.postJSON(ctx, c.apiBase, map[string]string{"type": "prx_logout"})
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errEmptyResponse
	}

	// The legacy backend always answers HTTP 200 and puts the real status in
	// the body, so the code has to be checked however healthy the transport
	// looked.
	var res legacyResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("decoding logout response (http %d): %w", status, err)
	}
	if res.Code != 0 {
		return fmt.Errorf("pluxee: prx_logout failed: %s (code %d)", res.Msg, res.Code)
	}
	return nil
}
