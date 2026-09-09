package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// printSession writes the captured jar to stdout so it can be piped to a cubit
// that this helper cannot reach directly.
//
// This prints a live session. It is opt-in via --print for exactly that reason.
func printSession(jar []sessionCookie) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"cookies": jar}); err != nil {
		return fmt.Errorf("printing the session: %w", err)
	}
	fmt.Fprintln(os.Stderr,
		"\nThis output is a live session. Post it to /api/v1/auth/session and then discard it.")
	return nil
}
