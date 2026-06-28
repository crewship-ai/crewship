package orchestrator

import (
	"strings"
	"testing"
	"time"
)

// codexDroidPrompt builds the [SYSTEM]/[USER]-delimited prompt that
// adapter_codex.go and adapter_droid.go produce for any non-empty system
// prompt. Mirrors the TrimSpace-then-wrap logic in those adapters so test
// expectations stay in sync with the actual adapter output.
func codexDroidPrompt(userMsg string) string {
	sys := strings.TrimSpace(crewshipSystemPreamble) // SystemPrompt empty in these tests
	return "[SYSTEM]\n" + sys + "\n\n[USER]\n" + userMsg
}

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
			// PR-A F1 invariant: --mcp-config is ALWAYS appended for
			// CLAUDE_CODE because setupMCPConfig auto-injects the
			// sidecar-hosted crewship-memory server. Path renders with
			// an empty AgentSlug here because the test doesn't set one.
			// Default (empty) profile normalises to CODING → curated built-in
			// allowlist via --tools. Harness-internal tools (TaskCreate, etc.)
			// are NOT in the list, so the agent can't reach for them.
			"claude code default",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", UserMessage: "hello"},
			[]string{"claude", "--print", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--verbose", "--bare", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--system-prompt", crewshipSystemPreamble, "--tools", "Read,Glob,Grep,Write,Edit,Bash,WebFetch,WebSearch,ToolSearch", "--max-turns", "50", "--mcp-config", "/crew/agents//.mcp.json", "--", "hello"},
		},
		{
			"claude code with system prompt",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", SystemPrompt: "be helpful", UserMessage: "hello"},
			[]string{"claude", "--print", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--verbose", "--bare", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--system-prompt", crewshipSystemPreamble + "be helpful", "--tools", "Read,Glob,Grep,Write,Edit,Bash,WebFetch,WebSearch,ToolSearch", "--max-turns", "50", "--mcp-config", "/crew/agents//.mcp.json", "--", "hello"},
		},
		{
			// MINIMAL is read-only: Read,Glob,Grep (the old value listed a
			// non-existent "Search" tool and omitted Glob — fixed here).
			"claude code minimal profile",
			AgentRunRequest{CLIAdapter: "CLAUDE_CODE", ToolProfile: "MINIMAL", UserMessage: "hello"},
			[]string{"claude", "--print", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--verbose", "--bare", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--system-prompt", crewshipSystemPreamble, "--tools", "Read,Glob,Grep,ToolSearch", "--max-turns", "50", "--mcp-config", "/crew/agents//.mcp.json", "--", "hello"},
		},
		{
			// Codex Rust port (npm @openai/codex 0.128.x): non-interactive
			// is `codex exec --json`, NOT `codex --quiet`. --sandbox needs
			// a value (workspace-write default for CODING profile).
			"codex cli default (CODING-equivalent → workspace-write)",
			AgentRunRequest{CLIAdapter: "CODEX_CLI", UserMessage: "hello"},
			[]string{"codex", "exec", "--json", "--sandbox", "workspace-write", "--", codexDroidPrompt("hello")},
		},
		{
			"codex cli minimal profile downgrades sandbox to read-only",
			AgentRunRequest{CLIAdapter: "CODEX_CLI", ToolProfile: "MINIMAL", UserMessage: "audit"},
			[]string{"codex", "exec", "--json", "--sandbox", "read-only", "--", codexDroidPrompt("audit")},
		},
		{
			"codex cli with model override",
			AgentRunRequest{CLIAdapter: "CODEX_CLI", LLMModel: "gpt-5", UserMessage: "hello"},
			[]string{"codex", "exec", "--json", "--sandbox", "workspace-write", "--model", "gpt-5", "--", codexDroidPrompt("hello")},
		},
		{
			// gemini-cli has no documented --system-instruction flag in
			// headless mode — the preamble is folded into the prompt body
			// with [SYSTEM] / [USER] delimiters.
			"gemini cli default",
			AgentRunRequest{CLIAdapter: "GEMINI_CLI", UserMessage: "hello"},
			// Gemini adapter does NOT TrimSpace the preamble (only Codex+Droid do)
			// because gemini-cli passes via -p flag and trailing whitespace is fine.
			[]string{"gemini", "-p", "[SYSTEM]\n" + crewshipSystemPreamble + "\n\n[USER]\nhello", "--output-format", "stream-json"},
		},
		{
			"gemini cli with system prompt + model",
			AgentRunRequest{CLIAdapter: "GEMINI_CLI", SystemPrompt: "be helpful", LLMModel: "gemini-2.5-pro", UserMessage: "hello"},
			[]string{"gemini", "-p", "[SYSTEM]\n" + crewshipSystemPreamble + "be helpful" + "\n\n[USER]\nhello", "--output-format", "stream-json", "-m", "gemini-2.5-pro"},
		},
		{
			// MINIMAL → read-only via Gemini's plan approval mode (its
			// --allowed-tools allowlist is deprecated).
			"gemini cli minimal profile is read-only (plan mode)",
			AgentRunRequest{CLIAdapter: "GEMINI_CLI", ToolProfile: "MINIMAL", UserMessage: "audit"},
			[]string{"gemini", "-p", "[SYSTEM]\n" + crewshipSystemPreamble + "\n\n[USER]\naudit", "--output-format", "stream-json", "--approval-mode", "plan"},
		},
		{
			// opencode flag is --format (NOT --output-format), values
			// "default" or "json". Adapter passes --format json.
			"opencode default",
			AgentRunRequest{CLIAdapter: "OPENCODE", UserMessage: "hello"},
			[]string{"opencode", "run", "--format", "json", "--", codexDroidPrompt("hello")},
		},
		{
			"opencode with provider/model namespaced model",
			AgentRunRequest{CLIAdapter: "OPENCODE", LLMModel: "anthropic/claude-sonnet-4-6", UserMessage: "hello"},
			[]string{"opencode", "run", "--format", "json", "--model", "anthropic/claude-sonnet-4-6", "--", codexDroidPrompt("hello")},
		},
		{
			// Cursor headless: --force prevents the agent from blocking on
			// permission prompts in print mode (otherwise file edits hang).
			// --stream-partial-output produces incremental deltas.
			// --approve-mcps NOT added here because no MCP source configured.
			"cursor cli default (no MCP)",
			AgentRunRequest{CLIAdapter: "CURSOR_CLI", UserMessage: "hello"},
			[]string{"cursor-agent", "-p", "--output-format", "stream-json", "--stream-partial-output", "--force", "--", codexDroidPrompt("hello")},
		},
		{
			"cursor cli with model override (no MCP)",
			AgentRunRequest{CLIAdapter: "CURSOR_CLI", LLMModel: "gpt-5.5", UserMessage: "hello"},
			[]string{"cursor-agent", "-p", "--output-format", "stream-json", "--stream-partial-output", "--force", "-m", "gpt-5.5", "--", codexDroidPrompt("hello")},
		},
		{
			// When MCP is configured, --approve-mcps is needed or Cursor's
			// -p mode silently skips MCP servers (forum #143045 + #148397).
			"cursor cli with MCP gets --approve-mcps",
			AgentRunRequest{CLIAdapter: "CURSOR_CLI", UserMessage: "hello", CrewMCPConfigJSON: `{"mcpServers":{"x":{"command":"npx"}}}`},
			[]string{"cursor-agent", "-p", "--output-format", "stream-json", "--stream-partial-output", "--force", "--approve-mcps", "--", codexDroidPrompt("hello")},
		},
		{
			// Default policy is medium because the API normalises empty
			// ToolProfile to "CODING" before BuildCLICommand sees it.
			// See exec.go FACTORY_DROID case for the rationale.
			"factory droid default (no profile) is medium",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", UserMessage: "fix the bug"},
			[]string{"droid", "exec", "--auto", "medium", "-o", "stream-json", "--", codexDroidPrompt("fix the bug")},
		},
		{
			"factory droid coding profile is medium",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", ToolProfile: "CODING", UserMessage: "ship the feature"},
			[]string{"droid", "exec", "--auto", "medium", "-o", "stream-json", "--", codexDroidPrompt("ship the feature")},
		},
		{
			"factory droid minimal profile downgrades to low",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", ToolProfile: "MINIMAL", UserMessage: "audit only"},
			[]string{"droid", "exec", "--auto", "low", "-o", "stream-json", "--", codexDroidPrompt("audit only")},
		},
		{
			"factory droid full profile escalates to high",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", ToolProfile: "FULL", UserMessage: "ship it"},
			[]string{"droid", "exec", "--auto", "high", "-o", "stream-json", "--", codexDroidPrompt("ship it")},
		},
		{
			"factory droid with model",
			AgentRunRequest{CLIAdapter: "FACTORY_DROID", ToolProfile: "MINIMAL", LLMModel: "claude-sonnet-4-6", UserMessage: "review"},
			[]string{"droid", "exec", "--auto", "low", "-o", "stream-json", "--model", "claude-sonnet-4-6", "--", codexDroidPrompt("review")},
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
