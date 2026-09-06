package providerlogin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeJWT builds an unsigned JWT with the given claims. Only ever fake
// material: the parser never verifies a signature, and no test here may
// carry a real token.
func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func chatgptToken(t *testing.T, plan string, exp int64) string {
	t.Helper()
	return fakeJWT(t, map[string]any{
		"exp": float64(exp),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  plan,
			"chatgpt_account_id": "acct-1",
		},
	})
}

func codexAuthJSON(t *testing.T, plan string) string {
	t.Helper()
	return `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id.tok.en","access_token":"` +
		chatgptToken(t, plan, 1789000000) +
		`","refresh_token":"rt.REAL-SECRET","account_id":"acct-1"},"last_refresh":"2026-08-29T20:31:08Z"}`
}

func TestSplit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		provider     string
		mode         string
		value        string
		wantErr      string
		wantMode     string
		wantAccess   string
		wantRefresh  string
		wantIDToken  string
		wantAccount  string
		wantPlan     string
		wantExpires  string
		wantSupports bool
	}{
		{
			name: "codex auth.json becomes parts", provider: "OPENAI", mode: "subscription", value: codexAuthJSON(t, "plus"),
			wantMode: "subscription", wantAccess: chatgptToken(t, "plus", 1789000000), wantRefresh: "rt.REAL-SECRET",
			wantIDToken: "id.tok.en", wantAccount: "acct-1", wantPlan: "plus", wantExpires: "2026-09-10T00:26:40Z", wantSupports: true,
		},
		{
			name: "mode inferred from an auth.json", provider: "openai", mode: "", value: codexAuthJSON(t, "pro"),
			wantMode: "subscription", wantRefresh: "rt.REAL-SECRET", wantPlan: "pro", wantExpires: "2026-09-10T00:26:40Z", wantSupports: true,
			wantAccess: chatgptToken(t, "pro", 1789000000), wantIDToken: "id.tok.en", wantAccount: "acct-1",
		},
		{
			name: "openai subscription rejects a bare key", provider: "OPENAI", mode: "subscription", value: "sk-proj-abc",
			wantErr: "paste the whole ~/.codex/auth.json",
		},
		{
			name: "openai api key stays opaque", provider: "OPENAI", mode: "api_key", value: "sk-proj-abc",
			wantMode: "api_key", wantAccess: "sk-proj-abc",
		},
		{
			name: "openai key with mode inferred", provider: "OPENAI", mode: "", value: "sk-proj-abc",
			wantMode: "api_key", wantAccess: "sk-proj-abc",
		},
		{
			name: "anthropic setup-token", provider: "ANTHROPIC", mode: "subscription", value: " sk-ant-oat01-xyz \n",
			wantMode: "subscription", wantAccess: "sk-ant-oat01-xyz",
		},
		{
			name: "anthropic setup-token inferred", provider: "ANTHROPIC", mode: "", value: "sk-ant-oat01-xyz",
			wantMode: "subscription", wantAccess: "sk-ant-oat01-xyz",
		},
		{
			name: "anthropic subscription rejects an api key", provider: "ANTHROPIC", mode: "subscription", value: "sk-ant-api03-xyz",
			wantErr: "claude setup-token",
		},
		{
			name: "anthropic api key", provider: "ANTHROPIC", mode: "api_key", value: "sk-ant-api03-xyz",
			wantMode: "api_key", wantAccess: "sk-ant-api03-xyz",
		},
		{
			name: "google subscription rejects incomplete import", provider: "GOOGLE", mode: "subscription", value: "{}",
			wantErr: "no access_token",
		},
		{
			name: "cursor api key", provider: "CURSOR", mode: "api_key", value: "cur_123",
			wantMode: "api_key", wantAccess: "cur_123",
		},
		{
			name: "unknown provider", provider: "GITHUB", mode: "api_key", value: "ghp_x",
			wantErr: "not a model provider",
		},
		{
			name: "bad mode", provider: "OPENAI", mode: "seat", value: "sk-x",
			wantErr: "mode must be",
		},
		{
			name: "empty value", provider: "OPENAI", mode: "api_key", value: "  ",
			wantErr: "value is required",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := Split(c.provider, c.mode, c.value)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Split: %v", err)
			}
			if got.Mode != c.wantMode || got.AccessToken != c.wantAccess || got.RefreshToken != c.wantRefresh ||
				got.IDToken != c.wantIDToken || got.AccountID != c.wantAccount || got.Plan != c.wantPlan {
				t.Errorf("Split = %+v", got)
			}
			gotExp := ""
			if !got.ExpiresAt.IsZero() {
				gotExp = got.ExpiresAt.UTC().Format(time.RFC3339)
			}
			if gotExp != c.wantExpires {
				t.Errorf("ExpiresAt = %q, want %q", gotExp, c.wantExpires)
			}
			if got.RefreshSupported() != c.wantSupports {
				t.Errorf("RefreshSupported = %v, want %v", got.RefreshSupported(), c.wantSupports)
			}
			if got.Provider != strings.ToUpper(strings.TrimSpace(c.provider)) {
				t.Errorf("Provider = %q", got.Provider)
			}
		})
	}
}

func TestIsProviderAndDelivery(t *testing.T) {
	t.Parallel()
	for _, p := range append(Providers(), "openai") {
		if !IsProvider(p) {
			t.Errorf("IsProvider(%q) = false", p)
		}
		if _, err := Split(p, ModeAPIKey, "fixture-key"); err != nil {
			t.Errorf("API key for %s rejected: %v", p, err)
		}
		if DeliveryFor(p, ModeAPIKey).Target == "" {
			t.Errorf("provider %s has no delivery slot", p)
		}
	}
	for _, p := range []string{"GITHUB", "NONE", "", "OPENAI_COMPAT"} {
		if IsProvider(p) {
			t.Errorf("IsProvider(%q) = true", p)
		}
	}
	cases := []struct {
		provider, mode, wantKind, wantTarget string
	}{
		{"OPENAI", ModeSubscription, "file", ".codex/auth.json"},
		{"ANTHROPIC", ModeSubscription, "env", "CLAUDE_CODE_OAUTH_TOKEN"},
		{"OPENAI", ModeAPIKey, "env", "OPENAI_API_KEY"},
		{"ANTHROPIC", ModeAPIKey, "env", "ANTHROPIC_API_KEY"},
		{"GOOGLE", ModeAPIKey, "env", "GEMINI_API_KEY"},
		{"CURSOR", ModeAPIKey, "env", "CURSOR_API_KEY"},
		{"FACTORY", ModeAPIKey, "env", "FACTORY_API_KEY"},
		{"GROQ", ModeAPIKey, "env", "GROQ_API_KEY"},
		{"XAI", ModeAPIKey, "env", "XAI_API_KEY"},
		{"OPENROUTER", ModeAPIKey, "env", "OPENROUTER_API_KEY"},
	}
	for _, c := range cases {
		d := DeliveryFor(c.provider, c.mode)
		if d.Kind != c.wantKind || d.Target != c.wantTarget {
			t.Errorf("DeliveryFor(%s,%s) = %+v, want %s %s", c.provider, c.mode, d, c.wantKind, c.wantTarget)
		}
	}
}

func TestPlanLabel(t *testing.T) {
	t.Parallel()
	cases := []struct{ provider, plan, want string }{
		{"OPENAI", "plus", "ChatGPT Plus"},
		{"OPENAI", "", "ChatGPT"},
		{"ANTHROPIC", "max", "Claude Max"},
		{"ANTHROPIC", "", "Claude"},
		{"GOOGLE", "", "Gemini"},
		{"CURSOR", "pro", "Cursor Pro"},
	}
	for _, c := range cases {
		if got := PlanLabel(c.provider, c.plan); got != c.want {
			t.Errorf("PlanLabel(%s,%s) = %q, want %q", c.provider, c.plan, got, c.want)
		}
	}
}

func TestAdapterProvider(t *testing.T) {
	t.Parallel()
	cases := []struct{ adapter, llmProvider, want string }{
		{"CLAUDE_CODE", "", "ANTHROPIC"},
		{"CODEX_CLI", "", "OPENAI"},
		{"GEMINI_CLI", "", "GOOGLE"},
		{"CURSOR_CLI", "", "CURSOR"},
		{"FACTORY_DROID", "", "FACTORY"},
		{"OPENCODE", "anthropic", "ANTHROPIC"},
		{"OPENCODE", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := AdapterProvider(c.adapter, c.llmProvider); got != c.want {
			t.Errorf("AdapterProvider(%s,%s) = %q, want %q", c.adapter, c.llmProvider, got, c.want)
		}
	}
}

// The OpenAI refresher posts the documented form to the token endpoint and
// returns the rotated pair. Never dials OpenAI: the endpoint is the test's.
func TestOpenAIRefresher_Refresh(t *testing.T) {
	t.Parallel()
	newAccess := chatgptToken(t, "plus", 1790000000)
	var gotForm map[string]string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotForm = map[string]string{}
		for k := range r.PostForm {
			gotForm[k] = r.PostForm.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + newAccess + `","refresh_token":"rt.NEW","id_token":"id.new","expires_in":864000}`))
	}))
	defer srv.Close()

	rf := NewOpenAIRefresher(srv.Client())
	rf.TokenURL = srv.URL
	res, err := rf.Refresh(context.Background(), "rt.OLD")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", gotContentType)
	}
	if gotForm["grant_type"] != "refresh_token" || gotForm["refresh_token"] != "rt.OLD" || gotForm["client_id"] != OpenAIClientID {
		t.Errorf("form = %v", gotForm)
	}
	if res.AccessToken != newAccess || res.RefreshToken != "rt.NEW" || res.IDToken != "id.new" {
		t.Errorf("result = %+v", res)
	}
	// Expiry comes from the token's own exp claim, not expires_in: the claim
	// is what Codex and the EXPIRING badge read.
	if got := res.ExpiresAt.UTC().Format(time.RFC3339); got != "2026-09-21T14:13:20Z" {
		t.Errorf("ExpiresAt = %s", got)
	}
}

func TestOpenAIRefresher_ErrorShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		status   int
		body     string
		wantErr  string
		wantPerm bool
	}{
		{"invalid grant is permanent", 400, `{"error":"invalid_grant","error_description":"refresh token expired"}`, "invalid_grant", true},
		{"reflected description is redacted", 400, `{"error":"invalid_grant","error_description":"rejected rt.OLD"}`, "invalid_grant", true},
		{"reflected error code is redacted", 400, `{"error":"rt.OLD"}`, "400", true},
		{"unauthorized is permanent", 401, `{"error":"invalid_client"}`, "invalid_client", true},
		{"server error is transient", 503, `upstream down`, "503", false},
		{"rate limit is transient", 429, `{"error":"rate_limited"}`, "429", false},
		{"missing access token", 200, `{"refresh_token":"rt.x"}`, "no access_token", false},
		{"not json", 200, `<html>`, "not JSON", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			rf := NewOpenAIRefresher(srv.Client())
			rf.TokenURL = srv.URL
			_, err := rf.Refresh(context.Background(), "rt.OLD")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
			if IsPermanent(err) != c.wantPerm {
				t.Errorf("IsPermanent = %v, want %v", IsPermanent(err), c.wantPerm)
			}
			// The refresh token must never be echoed in an error: errors land
			// in last_error and the log.
			if strings.Contains(err.Error(), "rt.OLD") {
				t.Errorf("error leaks the refresh token: %v", err)
			}
		})
	}
}

func TestRefreshDue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		expires time.Time
		lead    time.Duration
		want    bool
	}{
		{"far away", now.Add(200 * time.Hour), RefreshLead, false},
		{"inside the lead", now.Add(20 * time.Hour), RefreshLead, true},
		{"already expired", now.Add(-time.Hour), RefreshLead, true},
		{"run start lead is wider", now.Add(40 * time.Hour), RunStartLead, true},
		{"run start outside", now.Add(60 * time.Hour), RunStartLead, false},
		{"unknown expiry is due", time.Time{}, RefreshLead, true},
	}
	for _, c := range cases {
		if got := Due(c.expires, now, c.lead); got != c.want {
			t.Errorf("%s: Due = %v, want %v", c.name, got, c.want)
		}
	}
}
