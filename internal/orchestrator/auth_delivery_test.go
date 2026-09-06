package orchestrator

// AuthDelivery (docs/prd/provider-logins.md §5.2): one declaration per CLI
// adapter of where its account credential goes. The Claude/Codex/Cursor/
// Factory rows restate existing behaviour; the Gemini row adds the
// ~/.gemini/oauth_creds.json subscription delivery.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/geminiauth"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestAuthDelivery_Declarations(t *testing.T) {
	cases := []struct {
		adapter  string
		wantEnv  string
		wantFile string
		wantKind oauthKind
	}{
		{"CLAUDE_CODE", "CLAUDE_CODE_OAUTH_TOKEN", "", oauthAnthropic},
		{"CODEX_CLI", "", ".codex/auth.json", oauthOpenAI},
		{"GEMINI_CLI", "GEMINI_API_KEY", ".gemini/oauth_creds.json", oauthGoogle},
		{"CURSOR_CLI", "CURSOR_API_KEY", "", oauthNone},
		{"FACTORY_DROID", "FACTORY_API_KEY", "", oauthNone},
		{"OPENCODE", "", OpenCodeAuthFileRel, oauthNone},
		{"NOT_AN_ADAPTER", "", "", oauthNone},
	}
	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			d := getAdapter(tc.adapter).AuthDelivery()
			if d.Env != tc.wantEnv || d.File != tc.wantFile || d.Kind != tc.wantKind {
				t.Fatalf("AuthDelivery() = {Env:%q File:%q Kind:%v}, want {Env:%q File:%q Kind:%v}",
					d.Env, d.File, d.Kind, tc.wantEnv, tc.wantFile, tc.wantKind)
			}
			if d.FileDelivered() != (tc.wantFile != "") {
				t.Errorf("FileDelivered() = %v", d.FileDelivered())
			}
			// A file form always comes with a renderer, and a login kind
			// with a plan label; neither without the other half.
			if (d.Render != nil || d.RenderSet != nil) != d.FileDelivered() {
				t.Errorf("Render presence (%v) does not match File (%q)", d.Render != nil, d.File)
			}
			if (d.PlanLabel != nil) != (tc.wantKind != oauthNone) {
				t.Errorf("PlanLabel presence (%v) does not match Kind (%v)", d.PlanLabel != nil, tc.wantKind)
			}
		})
	}
}

// The renderers strip the refresh token — the property the whole design
// rests on, checked through the declaration rather than the package.
func TestAuthDelivery_RenderersStripRefreshToken(t *testing.T) {
	cases := []struct {
		adapter     string
		value       string
		realRefresh string
		placeholder string
	}{
		{"CODEX_CLI", codexLoginJSON(t, "plus"), "rt.REAL-SECRET", codexauth.PlaceholderRefreshToken},
		{"GEMINI_CLI", geminiLoginJSON(t, ""), "1//0gREAL-SECRET", geminiauth.PlaceholderRefreshToken},
	}
	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			d := getAdapter(tc.adapter).AuthDelivery()
			body, err := d.Render(tc.value, time.Now())
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if strings.Contains(string(body), tc.realRefresh) {
				t.Fatalf("rendered %s carries the real refresh token:\n%s", d.File, body)
			}
			if !strings.Contains(string(body), tc.placeholder) {
				t.Errorf("rendered %s lacks the placeholder:\n%s", d.File, body)
			}
			if _, err := d.Render("not-a-login", time.Now()); err == nil {
				t.Errorf("Render must refuse a value that is not the login file")
			}
		})
	}
}

// The env-delivered Claude path reads its variable from the declaration and
// keeps behaving exactly as before.
func TestAuthDelivery_ClaudeEnvUnchanged(t *testing.T) {
	cred := Credential{ID: "c1", EnvVarName: "WHATEVER", PlainValue: "sk-ant-oat01-x", Type: "AI_CLI_TOKEN", Provider: "ANTHROPIC"}
	if got := resolveEnvVar(&cred); got != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("resolveEnvVar(anthropic login) = %q", got)
	}
	if got := loginEnvVar(oauthOpenAI); got != "" {
		t.Errorf("loginEnvVar(openai) = %q, want empty (file-delivered)", got)
	}
	if got := loginEnvVar(oauthGoogle); got != "" {
		t.Errorf("loginEnvVar(google) = %q, want empty (file-delivered)", got)
	}
	if got := loginEnvVar(oauthNone); got != "" {
		t.Errorf("loginEnvVar(none) = %q", got)
	}
}

// --- Gemini ---------------------------------------------------------------

func geminiLoginJSON(t *testing.T, plan string) string {
	t.Helper()
	idClaims, _ := json.Marshal(map[string]any{"email": "jana@unify.cz"})
	idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(idClaims) + ".sig"
	m := map[string]any{
		"access_token":  "ya29.a0fake-access",
		"refresh_token": "1//0gREAL-SECRET",
		"scope":         geminiauth.DefaultScope,
		"token_type":    "Bearer",
		"id_token":      idToken,
		"expiry_date":   int64(1757150000000),
	}
	if plan != "" {
		m["plan"] = plan
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func geminiLoginReq(t *testing.T, plan string) AgentRunRequest {
	t.Helper()
	return AgentRunRequest{
		AgentID:       "a1",
		AgentSlug:     "researcher",
		CLIAdapter:    "GEMINI_CLI",
		LLMProvider:   "GOOGLE",
		LLMModel:      "gemini-2.5-pro",
		sidecarActive: true,
		Credentials: []Credential{
			{ID: "glogin", EnvVarName: "GEMINI_API_KEY", PlainValue: geminiLoginJSON(t, plan), Type: "AI_CLI_TOKEN", Provider: "GOOGLE"},
		},
	}
}

func TestCredentialOAuthKind_Google(t *testing.T) {
	cases := []struct {
		name string
		cred Credential
		want oauthKind
	}{
		{"google login", Credential{Type: "AI_CLI_TOKEN", Provider: "GOOGLE", PlainValue: "{}"}, oauthGoogle},
		{"google login, lower-case provider", Credential{Type: "AI_CLI_TOKEN", Provider: "google", PlainValue: "{}"}, oauthGoogle},
		{"google api key is not a login", Credential{Type: "API_KEY", Provider: "GOOGLE", PlainValue: "AIza"}, oauthNone},
		{"anthropic login unchanged", Credential{Type: "AI_CLI_TOKEN", Provider: "ANTHROPIC", PlainValue: "sk-ant-oat01-x"}, oauthAnthropic},
		{"openai login unchanged", Credential{Type: "AI_CLI_TOKEN", Provider: "OPENAI", PlainValue: "{}"}, oauthOpenAI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialOAuthKind(tc.cred); got != tc.want {
				t.Errorf("credentialOAuthKind = %v, want %v", got, tc.want)
			}
		})
	}
	if !oauthGoogle.fileDelivered() || !oauthOpenAI.fileDelivered() || oauthAnthropic.fileDelivered() || oauthNone.fileDelivered() {
		t.Error("fileDelivered: OpenAI and Google are files, Anthropic and none are not")
	}
}

func TestBuildEnvVarsSidecar_GeminiLogin(t *testing.T) {
	env := BuildEnvVarsSidecar(geminiLoginReq(t, "pro"), false)
	joined := strings.Join(env, "\n")

	if strings.Contains(joined, "1//0gREAL-SECRET") || strings.Contains(joined, "ya29.a0fake-access") {
		t.Fatalf("login material leaked into agent env:\n%s", joined)
	}
	if _, ok := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
		t.Errorf("a Google login must not be written to CLAUDE_CODE_OAUTH_TOKEN")
	}
	// Gemini prefers a key in the environment over its login file, so a
	// subscription run must carry neither dummy.
	if v, ok := envValue(env, "GEMINI_API_KEY"); ok {
		t.Errorf("GEMINI_API_KEY = %q, must be absent on a subscription run (a key overrides the login)", v)
	}
	if v, ok := envValue(env, "GOOGLE_API_KEY"); ok {
		t.Errorf("GOOGLE_API_KEY = %q, must be absent on a subscription run", v)
	}
	if got, ok := envValue(env, geminiauth.UseGCAEnv); !ok || got != "true" {
		t.Errorf("%s = %q (present=%v), want true: headless Gemini selects the login path with it", geminiauth.UseGCAEnv, got, ok)
	}
	if got, _ := envValue(env, "CREWSHIP_BILLING_MODE"); got != "flat_rate" {
		t.Errorf("CREWSHIP_BILLING_MODE = %q, want flat_rate", got)
	}
	if got, _ := envValue(env, "CREWSHIP_SUBSCRIPTION_PLAN"); got != "Google AI Pro" {
		t.Errorf("CREWSHIP_SUBSCRIPTION_PLAN = %q, want Google AI Pro", got)
	}
	// The Anthropic branch must not fire for a Google login.
	if got, ok := envValue(env, "ANTHROPIC_BASE_URL"); !ok || got != "http://127.0.0.1:9119" {
		t.Errorf("ANTHROPIC_BASE_URL = %q (present=%v): the Anthropic OAuth branch fired for a Google login", got, ok)
	}
}

// Without a login the Gemini run is exactly what it was: dummy keys for the
// sidecar to swap, metered billing, no GCA switch.
func TestBuildEnvVarsSidecar_GeminiApiKey_Unchanged(t *testing.T) {
	req := geminiLoginReq(t, "")
	req.Credentials = []Credential{{ID: "k", EnvVarName: "GEMINI_API_KEY", PlainValue: "AIzaREAL", Type: "API_KEY", Provider: "GOOGLE"}}
	env := BuildEnvVarsSidecar(req, false)
	if got, _ := envValue(env, "GEMINI_API_KEY"); got != "dummy-crewship-sidecar" {
		t.Errorf("GEMINI_API_KEY = %q, want the dummy", got)
	}
	if got, _ := envValue(env, "GOOGLE_API_KEY"); got != "dummy-crewship-sidecar" {
		t.Errorf("GOOGLE_API_KEY = %q, want the dummy", got)
	}
	if _, ok := envValue(env, geminiauth.UseGCAEnv); ok {
		t.Errorf("%s must not be set on an API-key run", geminiauth.UseGCAEnv)
	}
	if got, _ := envValue(env, "CREWSHIP_BILLING_MODE"); got != "metered" {
		t.Errorf("CREWSHIP_BILLING_MODE = %q, want metered", got)
	}
}

func TestBuildEnvVars_GeminiLogin_IsNotAnEnvVar(t *testing.T) {
	req := geminiLoginReq(t, "")
	for _, env := range [][]string{BuildEnvVars(req, nil), BuildEnvVars(req, &req.Credentials[0])} {
		joined := strings.Join(env, "\n")
		if strings.Contains(joined, "1//0gREAL-SECRET") {
			t.Fatalf("login leaked on the non-sidecar path:\n%s", joined)
		}
		if _, ok := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
			t.Errorf("Google login written to CLAUDE_CODE_OAUTH_TOKEN on the non-sidecar path")
		}
	}
}

func TestCredTypeToProvider_GeminiLoginStaysOut(t *testing.T) {
	cred := Credential{EnvVarName: "GEMINI_API_KEY", PlainValue: "{}", Type: "AI_CLI_TOKEN", Provider: "GOOGLE"}
	if got := credTypeToProvider(cred); got != "" {
		t.Errorf("credTypeToProvider(login) = %q, want \"\"", got)
	}
	if sc := buildSidecarCreds([]Credential{cred}, nil); len(sc) != 0 {
		t.Errorf("login reached the sidecar boot payload: %+v", sc)
	}
}

func TestAgentEnvCredentialExposures_ReportsGeminiLoginAsFile(t *testing.T) {
	var got []CredentialEnvExposure
	for _, e := range AgentEnvCredentialExposures(geminiLoginReq(t, ""), false) {
		if e.Type == "AI_CLI_TOKEN" {
			got = append(got, e)
		}
	}
	if len(got) != 1 || got[0].EnvVarName != geminiauth.FileRel || got[0].Actionable {
		t.Errorf("exposures = %+v, want one informational entry for %s", got, geminiauth.FileRel)
	}
}

func TestSyncLoginFile_Gemini(t *testing.T) {
	t.Run("writes rendered file without refresh token", func(t *testing.T) {
		rc := &countingContainer{}
		if err := syncLoginFile(context.Background(), rc, "ctr", geminiLoginReq(t, ""), slog.Default()); err != nil {
			t.Fatalf("sync: %v", err)
		}
		scripts := scriptsOf(rc)
		if len(scripts) != 1 {
			t.Fatalf("want one exec, got %d: %v", len(scripts), scripts)
		}
		if !strings.Contains(scripts[0], ".gemini/oauth_creds.json") || !strings.Contains(scripts[0], "chmod 600") {
			t.Errorf("script does not write .gemini/oauth_creds.json 0600: %s", scripts[0])
		}
		body := decodeWrittenFile(scripts[0], "access_token")
		if body == "" {
			t.Fatalf("could not find the rendered file in: %s", scripts[0])
		}
		if strings.Contains(body, "1//0gREAL-SECRET") {
			t.Fatalf("delivered oauth_creds.json carries the real refresh token:\n%s", body)
		}
		if !strings.Contains(body, geminiauth.PlaceholderRefreshToken) || !strings.Contains(body, `"expiry_date": 1757150000000`) {
			t.Errorf("delivered oauth_creds.json is not the rendered shape:\n%s", body)
		}
	})

	t.Run("removes stale file when no login", func(t *testing.T) {
		req := geminiLoginReq(t, "")
		req.Credentials = nil
		rc := &countingContainer{}
		if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err != nil {
			t.Fatalf("sync: %v", err)
		}
		scripts := scriptsOf(rc)
		if len(scripts) != 1 || !strings.Contains(scripts[0], "rm -f") || !strings.Contains(scripts[0], ".gemini/oauth_creds.json") {
			t.Errorf("want an rm of .gemini/oauth_creds.json, got %v", scripts)
		}
	})

	t.Run("rejects an unparseable login", func(t *testing.T) {
		req := geminiLoginReq(t, "")
		req.Credentials[0].PlainValue = "AIza-not-a-login"
		rc := &countingContainer{}
		if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err == nil {
			t.Fatal("expected an error for a value that is not oauth_creds.json")
		}
		if len(scriptsOf(rc)) != 0 {
			t.Errorf("nothing should have been written: %v", scriptsOf(rc))
		}
	})

	t.Run("no-op for an adapter without a file form", func(t *testing.T) {
		req := geminiLoginReq(t, "")
		req.CLIAdapter = "CLAUDE_CODE"
		rc := &countingContainer{}
		if err := syncLoginFile(context.Background(), rc, "ctr", req, slog.Default()); err != nil {
			t.Fatalf("sync: %v", err)
		}
		if len(scriptsOf(rc)) != 0 {
			t.Errorf("Claude Code has no login file; nothing should run: %v", scriptsOf(rc))
		}
	})
}

func TestPreparePreflightDirs_LoginFileFailureAbortsEveryAdapter(t *testing.T) {
	for _, adapter := range []string{"CODEX_CLI", "GEMINI_CLI", "OPENCODE"} {
		for _, operation := range []string{"file:", "rm:"} {
			if adapter == "OPENCODE" && operation == "rm:" {
				continue // the multi-provider renderer clears by writing {}
			}
			t.Run(adapter+"/"+operation, func(t *testing.T) {
				o, c, req := preflightFixture(t)
				req.CLIAdapter = adapter
				req.Credentials = nil
				if operation == "file:" {
					if adapter == "CODEX_CLI" {
						req.Credentials = codexLoginReq(t).Credentials
					} else {
						req.Credentials = geminiLoginReq(t, "").Credentials
					}
				}
				step := operation + getAdapter(adapter).AuthDelivery().File
				c.stdout = func(cfg provider.ExecConfig, stdin string) string {
					if out := healthySidecarStdout(cfg, stdin); out != "" {
						return out
					}
					if strings.Contains(stdin, step) {
						return preflightFailMarker + step + "\n"
					}
					return ""
				}
				_, _, err := o.preparePreflightDirs(context.Background(), req, nil, false, false, "run-1")
				if err == nil || !strings.Contains(err.Error(), "deliver "+adapter+" login") {
					t.Fatalf("failed login step must abort preflight, got %v", err)
				}
			})
		}
	}
}

// decodeWrittenFile finds the base64 body writeFileViaContainer echoed and
// returns it decoded, or "" when no decoded chunk contains marker.
func decodeWrittenFile(script, marker string) string {
	fields := strings.Fields(script)
	for i, f := range fields {
		if f == "echo" && i+1 < len(fields) {
			if b, err := base64.StdEncoding.DecodeString(fields[i+1]); err == nil && strings.Contains(string(b), marker) {
				return string(b)
			}
		}
	}
	return ""
}
