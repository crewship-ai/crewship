package orchestrator

import (
	"context"
	"log/slog"
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
			[]string{"claude", "--print", "--no-session-persistence", "--dangerously-skip-permissions", "--verbose", "--system-prompt", crewshipSystemPreamble, "hello"},
		},
		{
			"claude code with system prompt",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", SystemPrompt: "be helpful", UserMessage: "hello"},
			[]string{"claude", "--print", "--no-session-persistence", "--dangerously-skip-permissions", "--verbose", "--system-prompt", crewshipSystemPreamble + "be helpful", "hello"},
		},
		{
			"claude code minimal profile",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", ToolProfile: "MINIMAL", UserMessage: "hello"},
			[]string{"claude", "--print", "--no-session-persistence", "--dangerously-skip-permissions", "--verbose", "--system-prompt", crewshipSystemPreamble, "--tools", "Read,Search,Grep", "hello"},
		},
		{
			"codex cli",
			AgentRunRequest{CLIAdapter: "CODEX_CLI", UserMessage: "hello"},
			[]string{"codex", "--quiet", "hello"},
		},
		{
			"gemini cli",
			AgentRunRequest{CLIAdapter: "GEMINI_CLI", UserMessage: "hello"},
			[]string{"gemini", "-p", "hello"},
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
		TeamID:    "team-1",
		SessionID: "sess-1",
	}

	cred := &Credential{
		ID:         "cred-1",
		EnvVarName: "ANTHROPIC_API_KEY",
		PlainValue: "sk-test",
	}

	env := BuildEnvVars(req, cred)

	expected := map[string]bool{
		"CREWSHIP_AGENT_ID=agent-1":      false,
		"CREWSHIP_TEAM_ID=team-1":        false,
		"CREWSHIP_SESSION_ID=sess-1":     false,
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
		TeamID:    "team-1",
		SessionID: "sess-1",
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

func TestSetupClaudeCredentialsNilCred(t *testing.T) {
	err := setupClaudeCredentials(context.Background(), nil, "container-1", nil, slog.Default())
	if err != nil {
		t.Fatalf("expected nil error for nil cred, got: %v", err)
	}
}

func TestSetupClaudeCredentialsNonOAuthType(t *testing.T) {
	cred := &Credential{Type: "API_KEY", PlainValue: "sk-test"}
	err := setupClaudeCredentials(context.Background(), nil, "container-1", cred, slog.Default())
	if err != nil {
		t.Fatalf("expected nil error for non-OAuth type, got: %v", err)
	}
}
