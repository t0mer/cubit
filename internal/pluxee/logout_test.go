package pluxee

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Logout is the same call the web app makes when a user signs out. Without it
// the cookies stay valid at Pluxee until they expire on their own.
func TestLogoutPostsPrxLogout(t *testing.T) {
	var gotType string
	c := testClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		gotType, _ = body["type"].(string)
		io.WriteString(w, `{"code":0,"msg":"OK","http_code":200}`)
	})

	if err := c.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if gotType != "prx_logout" {
		t.Errorf("type = %q, want prx_logout", gotType)
	}
}

// The legacy backend always answers HTTP 200 and puts the real status in the
// body, so a non-zero code is a failure however healthy the transport looked.
func TestLogoutSurfacesANonZeroCode(t *testing.T) {
	c := testClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"code":177,"msg":"Can't find cookie token","http_code":401}`)
	})

	if err := c.Logout(context.Background()); err == nil {
		t.Fatal("Logout reported success for a non-zero code")
	}
}
