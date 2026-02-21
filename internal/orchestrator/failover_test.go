package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestCooldownManager(t *testing.T) {
	cm := NewCooldownManager()

	if cm.IsInCooldown("cred-1") {
		t.Fatal("expected no cooldown for unknown credential")
	}

	cm.MarkCooldown("cred-1", 5*time.Minute)
	if !cm.IsInCooldown("cred-1") {
		t.Fatal("expected cooldown active")
	}

	if cm.IsInCooldown("cred-2") {
		t.Fatal("expected no cooldown for other credential")
	}
}

func TestCooldownExpired(t *testing.T) {
	cm := NewCooldownManager()

	cm.MarkCooldown("cred-1", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	if cm.IsInCooldown("cred-1") {
		t.Fatal("expected cooldown expired")
	}
}

func TestClearExpired(t *testing.T) {
	cm := NewCooldownManager()
	cm.MarkCooldown("cred-1", 1*time.Millisecond)
	cm.MarkCooldown("cred-2", 1*time.Hour)
	time.Sleep(5 * time.Millisecond)

	cm.ClearExpired()

	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if _, ok := cm.cooldowns["cred-1"]; ok {
		t.Fatal("expected cred-1 cleared")
	}
	if _, ok := cm.cooldowns["cred-2"]; !ok {
		t.Fatal("expected cred-2 still present")
	}
}

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stderr   string
		expected bool
	}{
		{"rate limit detected", 1, "Error: rate limit exceeded", true},
		{"429 detected", 1, "HTTP 429 Too Many Requests", true},
		{"quota exceeded", 1, "Error: quota exceeded for model", true},
		{"billing limit", 1, "billing_hard_limit reached", true},
		{"normal error", 1, "Error: file not found", false},
		{"success exit code", 0, "rate limit", false},
		{"exit code 2", 2, "rate limit", false},
		{"empty stderr", 1, "", false},
		{"case insensitive", 1, "RATE_LIMIT exceeded", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRateLimitError(tt.exitCode, tt.stderr)
			if result != tt.expected {
				t.Fatalf("IsRateLimitError(%d, %q) = %v, want %v", tt.exitCode, tt.stderr, result, tt.expected)
			}
		})
	}
}

func TestBuildCLICommand(t *testing.T) {
	tests := []struct {
		name     string
		req      AgentRunRequest
		expected []string
	}{
		{
			"claude code default",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", UserMessage: "hello"},
			[]string{"claude", "--print", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--verbose", "--system-prompt", crewshipSystemPreamble, "--", "hello"},
		},
		{
			"claude code with system prompt",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", SystemPrompt: "be helpful", UserMessage: "hello"},
			[]string{"claude", "--print", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--verbose", "--system-prompt", crewshipSystemPreamble + "be helpful", "--", "hello"},
		},
		{
			"claude code minimal profile",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", ToolProfile: "MINIMAL", UserMessage: "hello"},
			[]string{"claude", "--print", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--verbose", "--system-prompt", crewshipSystemPreamble, "--tools", "Read,Search,Grep", "--", "hello"},
		},
		{
			"codex cli",
			AgentRunRequest{CLIAdapter: "CODEX_CLI", UserMessage: "hello"},
			[]string{"codex", "--quiet", "hello"},
		},
		{
			"gemini cli",
			AgentRunRequest{CLIAdapter: "GEMINI_CLI", UserMessage: "hello"},
			[]string{"gemini", "--system-instruction", crewshipSystemPreamble, "-p", "hello"},
		},
		{
			"gemini cli with system prompt",
			AgentRunRequest{CLIAdapter: "GEMINI_CLI", SystemPrompt: "be helpful", UserMessage: "hello"},
			[]string{"gemini", "--system-instruction", crewshipSystemPreamble + "be helpful", "-p", "hello"},
		},
		{
			"opencode",
			AgentRunRequest{CLIAdapter: "OPENCODE", UserMessage: "hello"},
			[]string{"opencode", "run", "hello"},
		},
		{
			"unknown defaults to claude",
			AgentRunRequest{CLIAdapter: "UNKNOWN", UserMessage: "hello"},
			[]string{"claude", "--print", "hello"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildCLICommand(tt.req)
			if len(result) != len(tt.expected) {
				t.Fatalf("got %v, want %v", result, tt.expected)
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Fatalf("got %v, want %v", result, tt.expected)
				}
			}
		})
	}
}

func TestBuildEnvVars(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "agent-1",
		CrewID:    "crew-1",
		ChatID: "chat-1",
	}

	cred := &Credential{
		ID:         "cred-1",
		EnvVarName: "ANTHROPIC_API_KEY",
		PlainValue: "sk-test",
	}

	env := BuildEnvVars(req, cred)

	expected := map[string]bool{
		"CREWSHIP_AGENT_ID=agent-1":      false,
		"CREWSHIP_CREW_ID=crew-1":        false,
		"CREWSHIP_CHAT_ID=chat-1":     false,
		"ANTHROPIC_API_KEY=sk-test":       false,
	}

	for _, e := range env {
		if _, ok := expected[e]; ok {
			expected[e] = true
		}
	}

	for k, found := range expected {
		if !found {
			t.Fatalf("missing env var: %s", k)
		}
	}
}

func TestBuildEnvVarsOAuthToken(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "agent-1",
		CrewID:    "crew-1",
		ChatID: "chat-1",
	}

	cred := &Credential{
		ID:         "cred-1",
		EnvVarName: "ANTHROPIC_API_KEY",
		PlainValue: "sk-ant-oat01-test",
		Type:       "AI_CLI_TOKEN",
	}

	env := BuildEnvVars(req, cred)

	found := false
	for _, e := range env {
		if e == "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-test" {
			found = true
		}
		if e == "ANTHROPIC_API_KEY=sk-ant-oat01-test" {
			t.Fatal("AI_CLI_TOKEN should NOT set ANTHROPIC_API_KEY")
		}
		if e == "ANTHROPIC_AUTH_TOKEN=sk-ant-oat01-test" {
			t.Fatal("AI_CLI_TOKEN should NOT set ANTHROPIC_AUTH_TOKEN anymore")
		}
	}
	if !found {
		t.Fatal("expected CLAUDE_CODE_OAUTH_TOKEN env var for AI_CLI_TOKEN credential")
	}
}

func TestResolveEnvVar(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		expected string
	}{
		{"API key stays as-is", Credential{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY"}, "ANTHROPIC_API_KEY"},
		{"AI_CLI_TOKEN maps to CLAUDE_CODE_OAUTH_TOKEN", Credential{Type: "AI_CLI_TOKEN", EnvVarName: "ANTHROPIC_API_KEY"}, "CLAUDE_CODE_OAUTH_TOKEN"},
		{"AI_CLI_TOKEN without env var name", Credential{Type: "AI_CLI_TOKEN", EnvVarName: ""}, "CLAUDE_CODE_OAUTH_TOKEN"},
		{"SECRET stays as-is", Credential{Type: "SECRET", EnvVarName: "MY_SECRET"}, "MY_SECRET"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveEnvVar(&tt.cred)
			if result != tt.expected {
				t.Fatalf("resolveEnvVar(%+v) = %q, want %q", tt.cred, result, tt.expected)
			}
		})
	}
}

func TestBuildEnvVarsSidecar(t *testing.T) {
	req := AgentRunRequest{
		AgentID: "agent-1",
		CrewID:  "crew-1",
		ChatID:  "chat-1",
		Credentials: []Credential{
			{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-real-secret"},
		},
	}

	env := BuildEnvVarsSidecar(req)

	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Must have proxy config
	if envMap["HTTP_PROXY"] != "http://127.0.0.1:9119" {
		t.Errorf("expected HTTP_PROXY=http://127.0.0.1:9119, got %q", envMap["HTTP_PROXY"])
	}
	if envMap["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:9119" {
		t.Errorf("expected ANTHROPIC_BASE_URL pointing to sidecar")
	}

	// Must have dummy API key, NOT the real one
	if envMap["ANTHROPIC_API_KEY"] == "sk-ant-real-secret" {
		t.Fatal("real credential MUST NOT appear in sidecar env vars")
	}
	if envMap["ANTHROPIC_API_KEY"] == "" {
		t.Fatal("dummy ANTHROPIC_API_KEY must be set for Claude Code")
	}

	// Must NOT have the plaintext credential anywhere
	for _, e := range env {
		if strings.Contains(e, "sk-ant-real-secret") {
			t.Fatalf("real credential leaked in env var: %s", e)
		}
	}
}

func TestCredTypeToProvider(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		expected string
	}{
		{"anthropic key", Credential{EnvVarName: "ANTHROPIC_API_KEY"}, "ANTHROPIC"},
		// AI_CLI_TOKEN (OAuth) is injected as CLAUDE_CODE_OAUTH_TOKEN env var directly,
		// not via sidecar CredStore (which only supports x-api-key injection).
		{"oauth token", Credential{Type: "AI_CLI_TOKEN", EnvVarName: "FOO"}, ""},
		{"openai key", Credential{EnvVarName: "OPENAI_API_KEY"}, "OPENAI"},
		{"google key", Credential{EnvVarName: "GOOGLE_API_KEY"}, "GOOGLE"},
		{"unknown", Credential{EnvVarName: "MY_CUSTOM_KEY"}, ""},
		{"github token", Credential{EnvVarName: "GITHUB_TOKEN"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := credTypeToProvider(tt.cred)
			if got != tt.expected {
				t.Errorf("credTypeToProvider(%+v) = %q, want %q", tt.cred, got, tt.expected)
			}
		})
	}
}
