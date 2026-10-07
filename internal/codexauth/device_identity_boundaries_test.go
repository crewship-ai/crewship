package codexauth

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/codexauth/codexauthtest"
)

func TestDeviceClientRejectsMixedDeviceAndAuthorizationIdentities(t *testing.T) {
	issuer := codexauthtest.NewFakeIssuer(t, "plus", "device@example.invalid")
	client := NewDeviceClient(issuer.URL(), nil)
	start, err := client.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	issuer.Authorize()
	for _, ids := range [][2]string{{"foreign-device", start.UserCode}, {start.DeviceAuthID, "foreign-user-code"}} {
		poll, err := client.Poll(t.Context(), ids[0], ids[1])
		if !errors.Is(err, ErrDeviceDenied) || poll.Authorized {
			t.Fatalf("mixed identity authorized: %#v %v", poll, err)
		}
	}
	valid, err := client.Poll(t.Context(), start.DeviceAuthID, start.UserCode)
	if err != nil || !valid.Authorized {
		t.Fatalf("valid authorization lost: %#v %v", valid, err)
	}
	for _, grant := range [][2]string{{"foreign-code", valid.Verifier}, {valid.Code, "foreign-verifier"}} {
		file, err := client.Exchange(t.Context(), grant[0], grant[1])
		if err == nil || !strings.Contains(err.Error(), "status 400") || file.AuthMode != "" {
			t.Fatalf("mixed grant yielded credentials: %#v %v", file, err)
		}
	}
	file, err := client.Exchange(t.Context(), valid.Code, valid.Verifier)
	if err != nil || file.AuthMode != "chatgpt" {
		t.Fatalf("valid grant rejected: %#v %v", file, err)
	}
	starts, polls, exchanges := issuer.Calls()
	if starts != 1 || polls != 3 || exchanges != 3 {
		t.Fatalf("unexpected protocol retries: %d %d %d", starts, polls, exchanges)
	}
}
