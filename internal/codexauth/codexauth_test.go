package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeJWT builds an unsigned JWT whose payload carries the given claims — the
// package never verifies signatures, so a fake signature part is enough.
func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func plusToken(t *testing.T) string {
	t.Helper()
	return fakeJWT(t, map[string]any{
		"exp": float64(1789000000),
		authClaim: map[string]any{
			"chatgpt_plan_type":  "plus",
			"chatgpt_account_id": "acct-from-claim",
		},
	})
}

func storedLogin(t *testing.T, accountID string) string {
	t.Helper()
	f := File{
		AuthMode: "chatgpt",
		Tokens: Tokens{
			IDToken:      "id.tok.en",
			AccessToken:  plusToken(t),
			RefreshToken: "rt.REAL-SECRET-refresh-token",
			AccountID:    accountID,
		},
		LastRefresh: "2026-08-29T20:31:08.072235555Z",
	}
	b, _ := json.Marshal(f)
	return string(b)
}

func TestIsLogin(t *testing.T) {
	cases := []struct {
		typ, provider string
		want          bool
	}{
		{"AI_CLI_TOKEN", "OPENAI", true},
		{"AI_CLI_TOKEN", "openai", true},
		{"AI_CLI_TOKEN", " OpenAI ", true},
		{"AI_CLI_TOKEN", "ANTHROPIC", false},
		{"AI_CLI_TOKEN", "", false},
		{"API_KEY", "OPENAI", false},
		{"CLI_TOKEN", "OPENAI", false},
	}
	for _, c := range cases {
		t.Run(c.typ+"/"+strings.TrimSpace(c.provider), func(t *testing.T) {
			if got := IsLogin(c.typ, c.provider); got != c.want {
				t.Errorf("IsLogin(%q,%q) = %v, want %v", c.typ, c.provider, got, c.want)
			}
		})
	}
}

func TestParse_AcceptsCodexAuthJSON(t *testing.T) {
	f, err := Parse(storedLogin(t, "acct-1"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Tokens.AccountID != "acct-1" || f.Tokens.IDToken != "id.tok.en" || f.Tokens.RefreshToken != "rt.REAL-SECRET-refresh-token" {
		t.Errorf("parsed tokens lost fields: %+v", f.Tokens)
	}
	if f.AuthMode != "chatgpt" {
		t.Errorf("auth_mode = %q", f.AuthMode)
	}
}

func TestParse_FillsAccountIDFromClaim(t *testing.T) {
	f, err := Parse(storedLogin(t, ""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Tokens.AccountID != "acct-from-claim" {
		t.Errorf("account_id = %q, want the claim's", f.Tokens.AccountID)
	}
}

func TestParse_RejectsWhatCodexWouldReject(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"bare token":       plusToken(t),
		"not json":         "{nope",
		"no access token":  `{"tokens":{"id_token":"x","refresh_token":"y","account_id":"z"}}`,
		"no id token":      `{"tokens":{"access_token":"x","refresh_token":"y","account_id":"z"}}`,
		"no account, none": `{"tokens":{"access_token":"a.b.c","id_token":"x","refresh_token":"y"}}`,
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(v); err == nil {
				t.Errorf("Parse accepted %q", v)
			}
			if ShapeError(v) == "" {
				t.Error("ShapeError empty")
			}
		})
	}
	if msg := ShapeError(storedLogin(t, "acct-1")); msg != "" {
		t.Errorf("valid login rejected: %s", msg)
	}
}

// The delivered file carries the placeholder, never the stored refresh token,
// and everything Codex requires is present.
func TestRender_ReplacesRefreshTokenAndStampsNow(t *testing.T) {
	f, err := Parse(storedLogin(t, "acct-1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	out, err := Render(f, now)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "rt.REAL-SECRET-refresh-token") {
		t.Fatalf("rendered file leaks the stored refresh token:\n%s", s)
	}
	var back File
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("rendered file is not JSON: %v\n%s", err, s)
	}
	if back.Tokens.RefreshToken != PlaceholderRefreshToken {
		t.Errorf("refresh_token = %q, want placeholder", back.Tokens.RefreshToken)
	}
	if back.Tokens.AccessToken != f.Tokens.AccessToken || back.Tokens.IDToken != "id.tok.en" || back.Tokens.AccountID != "acct-1" {
		t.Errorf("rendered tokens changed: %+v", back.Tokens)
	}
	if back.AuthMode != "chatgpt" {
		t.Errorf("auth_mode = %q", back.AuthMode)
	}
	if !strings.HasPrefix(back.LastRefresh, "2026-09-06T09:00:00") {
		t.Errorf("last_refresh = %q, want stamped now", back.LastRefresh)
	}
	// Codex reads OPENAI_API_KEY from the file and prefers it when set; the
	// rendered file must carry the explicit null `codex login` writes.
	if !strings.Contains(s, `"OPENAI_API_KEY": null`) {
		t.Errorf("OPENAI_API_KEY not rendered as null:\n%s", s)
	}
}

func TestPlanLabelAndExpiry(t *testing.T) {
	login := storedLogin(t, "acct-1")
	if got := PlanType(login); got != "plus" {
		t.Errorf("PlanType(login) = %q", got)
	}
	if got := PlanLabel(login); got != "ChatGPT Plus" {
		t.Errorf("PlanLabel(login) = %q", got)
	}
	if got := PlanLabel(plusToken(t)); got != "ChatGPT Plus" {
		t.Errorf("PlanLabel(bare token) = %q", got)
	}
	if got := PlanLabel("not-a-jwt"); got != "ChatGPT" {
		t.Errorf("PlanLabel(garbage) = %q, want plain ChatGPT", got)
	}
	if got := PlanLabel(fakeJWT(t, map[string]any{authClaim: map[string]any{"chatgpt_plan_type": "edu"}})); got != "ChatGPT Edu" {
		t.Errorf("PlanLabel(unknown plan) = %q", got)
	}
	exp, ok := AccessTokenExpiry(login)
	if !ok || exp.Unix() != 1789000000 {
		t.Errorf("AccessTokenExpiry = %v, %v", exp, ok)
	}
	if _, ok := AccessTokenExpiry("garbage"); ok {
		t.Error("AccessTokenExpiry accepted garbage")
	}
}
