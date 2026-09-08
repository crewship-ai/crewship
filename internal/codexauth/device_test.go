package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth/codexauthtest"
)

func TestDeviceClient_FullFlow(t *testing.T) {
	issuer := codexauthtest.NewFakeIssuer(t, "plus", "jana@unify.cz")
	c := NewDeviceClient(issuer.URL(), nil)
	ctx := context.Background()

	start, err := c.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if start.DeviceAuthID != "dev-auth-123" || start.UserCode != "ABCD-EFGHJ" {
		t.Errorf("Start = %+v", start)
	}
	if start.VerificationURL != issuer.URL()+"/codex/device" {
		t.Errorf("VerificationURL = %q, want the issuer's /codex/device page", start.VerificationURL)
	}
	if start.Interval != time.Second {
		t.Errorf("Interval = %v, want 1s (the STRING \"1\" parsed)", start.Interval)
	}

	// Not yet: 403 is "keep polling", not an error.
	p, err := c.Poll(ctx, start.DeviceAuthID, start.UserCode)
	if err != nil || p.Authorized || p.RetryAfter != 0 {
		t.Fatalf("Poll before authorise = %+v, %v; want pending", p, err)
	}

	issuer.Authorize()
	p, err = c.Poll(ctx, start.DeviceAuthID, start.UserCode)
	if err != nil || !p.Authorized || p.Code != "authz-code-xyz" || p.Verifier != "verifier-abc" {
		t.Fatalf("Poll after authorise = %+v, %v", p, err)
	}

	f, err := c.Exchange(ctx, p.Code, p.Verifier)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if issuer.LastExchange["redirect_uri"] != issuer.URL()+"/deviceauth/callback" {
		t.Errorf("redirect_uri = %q", issuer.LastExchange["redirect_uri"])
	}
	if issuer.LastExchange["client_id"] != ClientID {
		t.Errorf("client_id = %q", issuer.LastExchange["client_id"])
	}
	if f.AuthMode != "chatgpt" || f.OpenAIAPIKey != nil || f.Tokens.AccountID != "acct-device-1" ||
		f.Tokens.RefreshToken != "rt.FAKE-REFRESH-SECRET" || f.LastRefresh == "" {
		t.Errorf("assembled file = %+v", f)
	}

	// What the flow assembles is exactly what a pasted `codex login` file
	// parses to, renders from, and labels as.
	raw, _ := json.Marshal(f)
	if _, err := Parse(string(raw)); err != nil {
		t.Fatalf("assembled file does not Parse: %v", err)
	}
	if got := PlanLabel(string(raw)); got != "ChatGPT Plus" {
		t.Errorf("PlanLabel = %q", got)
	}
	if got := Email(string(raw)); got != "jana@unify.cz" {
		t.Errorf("Email = %q", got)
	}
	body, err := Render(f, time.Now())
	if err != nil || strings.Contains(string(body), "rt.FAKE-REFRESH-SECRET") {
		t.Errorf("Render = %v: %s", err, body)
	}
}

func TestDeviceClient_Start_Errors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr error
	}{
		{"not enabled", http.StatusNotFound, ErrDeviceNotEnabled},
		{"server error", http.StatusInternalServerError, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer := codexauthtest.NewFakeIssuer(t, "plus", "")
			issuer.SetStartStatus(tc.status)
			_, err := NewDeviceClient(issuer.URL(), nil).Start(context.Background())
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestDeviceClient_Poll_Statuses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		retryAfter string
		wantRetry  time.Duration
		wantDenied bool
	}{
		{"403 pending", http.StatusForbidden, "", 0, false},
		{"404 pending", http.StatusNotFound, "", 0, false},
		{"429 slow down with Retry-After", http.StatusTooManyRequests, "7", 7 * time.Second, false},
		{"429 slow down without header", http.StatusTooManyRequests, "", 0, false},
		{"503 transient", http.StatusServiceUnavailable, "", 0, false},
		{"400 denied", http.StatusBadRequest, "", 0, true},
		{"401 denied", http.StatusUnauthorized, "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer := codexauthtest.NewFakeIssuer(t, "plus", "")
			issuer.SetPollStatus(tc.status, tc.retryAfter)
			p, err := NewDeviceClient(issuer.URL(), nil).Poll(context.Background(), "dev-auth-123", "ABCD-EFGHJ")
			if tc.wantDenied {
				if !errors.Is(err, ErrDeviceDenied) {
					t.Fatalf("err = %v, want ErrDeviceDenied", err)
				}
				return
			}
			if err != nil || p.Authorized {
				t.Fatalf("Poll = %+v, %v; want pending", p, err)
			}
			if p.RetryAfter != tc.wantRetry {
				t.Errorf("RetryAfter = %v, want %v", p.RetryAfter, tc.wantRetry)
			}
		})
	}
}

func TestParseInterval(t *testing.T) {
	cases := map[string]time.Duration{
		`"5"`:    5 * time.Second,
		`5`:      5 * time.Second,
		`" 12 "`: 12 * time.Second,
		`"0"`:    DefaultPollInterval,
		`"abc"`:  DefaultPollInterval,
		``:       DefaultPollInterval,
	}
	for raw, want := range cases {
		if got := parseInterval(json.RawMessage(raw)); got != want {
			t.Errorf("parseInterval(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestAssembleFile(t *testing.T) {
	idWithAccount := codexauthtest.FakeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-id"}})
	idWithout := codexauthtest.FakeJWT(t, map[string]any{"email": "x@y"})
	accessWithAccount := codexauthtest.FakeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-access"}})
	accessWithout := codexauthtest.FakeJWT(t, map[string]any{"exp": 1.0})

	cases := []struct {
		name        string
		id, access  string
		refresh     string
		wantAccount string
		wantErr     bool
	}{
		{"account from id_token", idWithAccount, accessWithout, "rt.x", "acct-id", false},
		{"account falls back to access token", idWithout, accessWithAccount, "rt.x", "acct-access", false},
		{"no account anywhere", idWithout, accessWithout, "rt.x", "", true},
		{"missing refresh token", idWithAccount, accessWithAccount, "", "", true},
		{"missing id token", "", accessWithAccount, "rt.x", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := AssembleFile(tc.id, tc.access, tc.refresh)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("AssembleFile: %v", err)
			}
			if f.Tokens.AccountID != tc.wantAccount {
				t.Errorf("AccountID = %q, want %q", f.Tokens.AccountID, tc.wantAccount)
			}
		})
	}
}
