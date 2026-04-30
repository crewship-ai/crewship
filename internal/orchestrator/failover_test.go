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

// TestIsInCooldownPrunesExpired verifies the self-prune path: once an
// entry is observed expired by a reader, it should be evicted from the
// internal map so cooldowns can't grow without bound over the process
// lifetime. Without this, ClearExpired had no production caller and
// every rate-limit ever recorded leaked one map entry forever.
func TestIsInCooldownPrunesExpired(t *testing.T) {
	cm := NewCooldownManager()
	// Use a negative duration so the cooldown is expired the moment it lands —
	// deterministic, no sleep, no flake under CI load.
	cm.MarkCooldown("cred-1", -1*time.Millisecond)

	// First read sees it's expired and prunes it.
	if cm.IsInCooldown("cred-1") {
		t.Fatal("expected expired cooldown to read as not-in-cooldown")
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if _, stillThere := cm.cooldowns["cred-1"]; stillThere {
		t.Fatal("expected expired entry to be pruned from the map after IsInCooldown")
	}
}

// TestIsInCooldownHonorsConcurrentRefresh verifies that if a concurrent
// MarkCooldown refreshes an entry to a future time between the RLock release
// and the write Lock acquire in IsInCooldown, the function returns true (the
// credential is still in cooldown) rather than reporting it expired.
//
// Regression: previously the re-check branch returned false unconditionally,
// even when the rechecked timestamp was now in the future, which could
// trigger premature failover.
func TestIsInCooldownHonorsConcurrentRefresh(t *testing.T) {
	cm := NewCooldownManager()
	// Seed an already-expired entry — the "until" read at the top of
	// IsInCooldown will see this and decide to enter the prune branch.
	cm.MarkCooldown("cred-1", -1*time.Millisecond)

	// Simulate the concurrent MarkCooldown that races past the RLock release
	// by overwriting the entry to a future time before we call IsInCooldown.
	cm.mu.Lock()
	cm.cooldowns["cred-1"] = time.Now().Add(1 * time.Hour)
	cm.mu.Unlock()

	if !cm.IsInCooldown("cred-1") {
		t.Fatal("expected refreshed cooldown to be reported as in-cooldown")
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if _, stillThere := cm.cooldowns["cred-1"]; !stillThere {
		t.Fatal("refreshed entry was incorrectly pruned despite future expiry")
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
		// Real-world CLI exits for 429s vary; previously only exit 1 was
		// accepted. Now any non-zero exit code with a matching stderr
		// pattern triggers cooldown, so the rate-limit failover engages
		// after a SIGKILL (137) or usage error (2) too.
		{"exit code 2 with rate limit stderr", 2, "rate limit", true},
		{"timeout exit code 124", 124, "Error: HTTP 429", true},
		{"OOM exit code 137", 137, "rate_limit reached", true},
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
			"cursor cli default",
			AgentRunRequest{CLIAdapter: "CURSOR_CLI", UserMessage: "hello"},
			[]string{"cursor-agent", "-p", "--output-format", "stream-json", "--", "hello"},
		},
		{
			"cursor cli with model override",
			AgentRunRequest{CLIAdapter: "CURSOR_CLI", LLMModel: "gpt-5.5", UserMessage: "hello"},
			[]string{"cursor-agent", "-p", "--output-format", "stream-json", "-m", "gpt-5.5", "--", "hello"},
		},
		{
			"factory droid default low autonomy",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", UserMessage: "fix the bug"},
			[]string{"droid", "exec", "--auto", "low", "fix the bug"},
		},
		{
			"factory droid coding profile bumps to medium",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", ToolProfile: "CODING", UserMessage: "ship the feature"},
			[]string{"droid", "exec", "--auto", "medium", "ship the feature"},
		},
		{
			"factory droid with model",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", LLMModel: "claude-sonnet-4-6", UserMessage: "review"},
			[]string{"droid", "exec", "--auto", "low", "--model", "claude-sonnet-4-6", "review"},
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
		AgentID: "agent-1",
		CrewID:  "crew-1",
		ChatID:  "chat-1",
	}

	cred := &Credential{
		ID:         "cred-1",
		EnvVarName: "ANTHROPIC_API_KEY",
		PlainValue: "sk-test",
	}

	env := BuildEnvVars(req, cred)

	expected := map[string]bool{
		"CREWSHIP_AGENT_ID=agent-1": false,
		"CREWSHIP_CREW_ID=crew-1":   false,
		"CREWSHIP_CHAT_ID=chat-1":   false,
		"ANTHROPIC_API_KEY=sk-test": false,
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
		AgentID: "agent-1",
		CrewID:  "crew-1",
		ChatID:  "chat-1",
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
		{"API key stays as-is", Credential{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-api01-real"}, "ANTHROPIC_API_KEY"},
		{"AI_CLI_TOKEN maps to CLAUDE_CODE_OAUTH_TOKEN", Credential{Type: "AI_CLI_TOKEN", EnvVarName: "ANTHROPIC_API_KEY"}, "CLAUDE_CODE_OAUTH_TOKEN"},
		{"AI_CLI_TOKEN without env var name", Credential{Type: "AI_CLI_TOKEN", EnvVarName: ""}, "CLAUDE_CODE_OAUTH_TOKEN"},
		{"SECRET stays as-is", Credential{Type: "SECRET", EnvVarName: "MY_SECRET"}, "MY_SECRET"},
		{"OAuth value stored as API_KEY type", Credential{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-oat01-token"}, "CLAUDE_CODE_OAUTH_TOKEN"},
		{"CLI_TOKEN stays as-is", Credential{Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN"}, "GH_TOKEN"},
		{"CLI_TOKEN GitLab", Credential{Type: "CLI_TOKEN", EnvVarName: "GITLAB_TOKEN"}, "GITLAB_TOKEN"},
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

	env := BuildEnvVarsSidecar(req, true)

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
