package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestSecWS_DialErrorRedactsToken proves that the error returned from the
// WebSocket dial path never carries the raw auth token. golang.org/x/net/
// websocket embeds the full dial URL (including ?token=…) in its error
// strings, so the helper that wraps that error MUST run it through the
// redactor before it can reach stderr / CI logs.
func TestSecWS_DialErrorRedactsToken(t *testing.T) {
	const token = "crewship_supersecret_token_value_1234567890"
	wsURL := "ws://example.test/ws?token=" + token

	// Simulate exactly what x/net/websocket does: it returns a *DialError
	// whose message contains the dial URL with the token in the query.
	rawDialErr := errors.New("websocket.Dial " + wsURL + ": dial tcp 1.2.3.4:80: connect: connection refused")

	wrapped := wrapDialError(rawDialErr)
	got := wrapped.Error()

	if strings.Contains(got, token) {
		t.Fatalf("dial error leaks raw token: %q", got)
	}
	if !strings.Contains(got, "websocket connect") {
		t.Fatalf("dial error lost its context prefix: %q", got)
	}
}
