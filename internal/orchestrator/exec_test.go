package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestHandleStreamJSONLine_TextDelta(t *testing.T) {
	o := New(nil, nil, slog.Default())

	line := `{"type":"stream_event","event":{"type":"delta","delta":{"type":"text_delta","text":"Hello "}}}`

	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "text" {
		t.Errorf("expected text event, got %q", events[0].Type)
	}
	if events[0].Content != "Hello " {
		t.Errorf("expected 'Hello ', got %q", events[0].Content)
	}
}

func TestHandleStreamJSONLine_ThinkingBlock(t *testing.T) {
	// With --include-partial-messages, text and thinking from assistant messages
	// are skipped (already delivered via stream_event deltas). Only tool_use/tool_result
	// blocks are emitted from assistant messages.
	line := `{"type":"assistant","content":[{"type":"thinking","thinking":"Let me analyze this code..."},{"type":"text","text":"Here is my answer"}]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	// text and thinking blocks from assistant messages are skipped to avoid duplication
	if len(events) != 0 {
		t.Fatalf("expected 0 events (text/thinking already streamed via deltas), got %d: %+v", len(events), events)
	}
}

func TestHandleStreamJSONLine_ThinkingDelta(t *testing.T) {
	// Claude Code stream events can include thinking_delta for streaming thinking
	line := `{"type":"stream_event","event":{"type":"delta","delta":{"type":"thinking_delta","thinking":"analyzing..."}}}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "thinking" {
		t.Errorf("expected thinking event, got %q", events[0].Type)
	}
	if events[0].Content != "analyzing..." {
		t.Errorf("expected 'analyzing...', got %q", events[0].Content)
	}

	// thinking_delta events should have streaming metadata for frontend differentiation
	meta, ok := events[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata to be map[string]interface{}, got %T", events[0].Metadata)
	}
	if meta["streaming"] != true {
		t.Errorf("expected streaming=true in metadata, got %v", meta["streaming"])
	}
}

func TestHandleStreamJSONLine_ToolCallMetadata(t *testing.T) {
	// tool_use blocks should emit structured metadata with tool_name and tool_id
	line := `{"type":"assistant","content":[{"type":"tool_use","id":"toolu_123","name":"Read","input":{"file_path":"main.go"}}]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "tool_call" {
		t.Errorf("expected tool_call event, got %q", events[0].Type)
	}
	if events[0].Content != "Read" {
		t.Errorf("expected content to be tool name 'Read', got %q", events[0].Content)
	}

	// Verify metadata has structured data
	meta, ok := events[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata to be map[string]interface{}, got %T", events[0].Metadata)
	}
	if meta["tool_name"] != "Read" {
		t.Errorf("expected tool_name 'Read', got %v", meta["tool_name"])
	}
	if meta["tool_id"] != "toolu_123" {
		t.Errorf("expected tool_id 'toolu_123', got %v", meta["tool_id"])
	}
	if meta["input"] == nil {
		t.Error("expected input to be present in metadata")
	}
}

func TestHandleStreamJSONLine_ToolResultContent(t *testing.T) {
	// tool_result blocks can carry a tool_use_id for correlation
	line := `{"type":"assistant","content":[{"type":"tool_result","tool_use_id":"toolu_123","text":"file contents here"}]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "tool_result" {
		t.Errorf("expected tool_result event, got %q", events[0].Type)
	}
	if events[0].Content != "file contents here" {
		t.Errorf("expected content, got %q", events[0].Content)
	}

	// Verify metadata has tool_use_id for correlation
	meta, ok := events[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata to be map[string]interface{}, got %T", events[0].Metadata)
	}
	if meta["tool_use_id"] != "toolu_123" {
		t.Errorf("expected tool_use_id 'toolu_123', got %v", meta["tool_use_id"])
	}
}

func TestHandleStreamJSONLine_NilHandler(t *testing.T) {
	o := New(nil, nil, slog.Default())
	// Should not panic with nil handler
	o.handleStreamJSONLine([]byte(`{"type":"assistant","content":[{"type":"text","text":"hello"}]}`), nil)
}

func TestHandleStreamJSONLine_InvalidJSON(t *testing.T) {
	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine([]byte("not json at all"), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 fallback event, got %d", len(events))
	}
	if events[0].Type != "text" {
		t.Errorf("expected fallback text event, got %q", events[0].Type)
	}
}

func TestHandleStreamJSONLine_Result(t *testing.T) {
	// "result" messages now emit metadata (cost, usage, duration) without duplicating text.
	o := New(nil, nil, slog.Default())
	line := `{"type":"result","subtype":"success","result":"Final answer","duration_ms":1234.5,"total_cost_usd":0.05,"num_turns":3,"usage":{"input_tokens":100,"output_tokens":50}}`

	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 result event, got %d", len(events))
	}
	if events[0].Type != "result" {
		t.Errorf("expected type 'result', got %q", events[0].Type)
	}
	meta, ok := events[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %T", events[0].Metadata)
	}
	if meta["total_cost_usd"] != 0.05 {
		t.Errorf("expected cost 0.05, got %v", meta["total_cost_usd"])
	}
	if meta["num_turns"] != 3 {
		t.Errorf("expected 3 turns, got %v", meta["num_turns"])
	}
	usage, ok := meta["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected usage map, got %T", meta["usage"])
	}
	if usage["input_tokens"] != float64(100) {
		t.Errorf("expected 100 input tokens, got %v", usage["input_tokens"])
	}
}

func TestHandleStreamJSONLine_SystemInit(t *testing.T) {
	o := New(nil, nil, slog.Default())
	line := `{"type":"system","subtype":"init","model":"claude-sonnet-4-20250514","tools":["Read","Write","Bash"],"cwd":"/home/user"}`

	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 system event, got %d", len(events))
	}
	if events[0].Type != "system" {
		t.Errorf("expected type 'system', got %q", events[0].Type)
	}
	if events[0].Content != "init" {
		t.Errorf("expected content 'init', got %q", events[0].Content)
	}
	meta, ok := events[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %T", events[0].Metadata)
	}
	if meta["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("expected model claude-sonnet-4-20250514, got %v", meta["model"])
	}
	tools, ok := meta["tools"].([]string)
	if !ok {
		t.Fatalf("expected tools []string, got %T", meta["tools"])
	}
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(tools))
	}
}

func TestHandleStreamJSONLine_ImageBlock(t *testing.T) {
	o := New(nil, nil, slog.Default())
	line := `{"type":"assistant","message":{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgoAAAA"}}]}}`

	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 image event, got %d", len(events))
	}
	if events[0].Type != "image" {
		t.Errorf("expected type 'image', got %q", events[0].Type)
	}
	if events[0].Content != "iVBORw0KGgoAAAA" {
		t.Errorf("expected base64 data, got %q", events[0].Content)
	}
	meta, ok := events[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %T", events[0].Metadata)
	}
	if meta["media_type"] != "image/png" {
		t.Errorf("expected media_type image/png, got %v", meta["media_type"])
	}
}

func TestHandleStreamJSONLine_ImageBlockInToolResult(t *testing.T) {
	o := New(nil, nil, slog.Default())
	line := `{"type":"tool","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"AQIDBA=="}}]}`

	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 image event, got %d", len(events))
	}
	if events[0].Type != "image" {
		t.Errorf("expected type 'image', got %q", events[0].Type)
	}
	if events[0].Content != "AQIDBA==" {
		t.Errorf("expected base64 data, got %q", events[0].Content)
	}
	meta := events[0].Metadata.(map[string]interface{})
	if meta["media_type"] != "image/jpeg" {
		t.Errorf("expected media_type image/jpeg, got %v", meta["media_type"])
	}
}

func TestHandleStreamJSONLine_ToolTypeMessage(t *testing.T) {
	// Claude Code emits tool results as top-level "tool" type messages
	line := `{"type":"tool","content":[{"type":"tool_result","tool_use_id":"toolu_abc","text":"file contents here"}]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "tool_result" {
		t.Errorf("expected tool_result event, got %q", events[0].Type)
	}

	meta, ok := events[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata, got %T", events[0].Metadata)
	}
	if meta["tool_use_id"] != "toolu_abc" {
		t.Errorf("expected tool_use_id 'toolu_abc', got %v", meta["tool_use_id"])
	}
}

func TestBuildEnvVarsAgentHome(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "viktor",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
	}
	env := BuildEnvVars(req, nil)

	found := false
	for _, e := range env {
		if e == "HOME=/crew/agents/viktor" {
			found = true
		}
		if e == "HOME=/home/agent" {
			t.Fatal("HOME must NOT be /home/agent anymore")
		}
	}
	if !found {
		t.Fatal("expected HOME=/crew/agents/viktor")
	}

	// Also check CREWSHIP_CREW_SHARED
	sharedFound := false
	for _, e := range env {
		if e == "CREWSHIP_CREW_SHARED=/crew/shared" {
			sharedFound = true
		}
	}
	if !sharedFound {
		t.Fatal("expected CREWSHIP_CREW_SHARED=/crew/shared")
	}
}

func TestBuildEnvVarsSidecarAgentHome(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "eva",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
		Credentials: []Credential{
			{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-test"},
		},
	}
	env := BuildEnvVarsSidecar(req, true)

	found := false
	for _, e := range env {
		if e == "HOME=/crew/agents/eva" {
			found = true
		}
		if e == "HOME=/home/agent" {
			t.Fatal("HOME must NOT be /home/agent in sidecar mode")
		}
	}
	if !found {
		t.Fatal("expected HOME=/crew/agents/eva in sidecar env")
	}

	// CREWSHIP_CREW_SHARED in sidecar mode too
	sharedFound := false
	for _, e := range env {
		if e == "CREWSHIP_CREW_SHARED=/crew/shared" {
			sharedFound = true
		}
	}
	if !sharedFound {
		t.Fatal("expected CREWSHIP_CREW_SHARED=/crew/shared in sidecar env")
	}
}

func TestConcurrentAgentHomeSeparation(t *testing.T) {
	// Two agents in the same crew must have different HOME dirs
	reqViktor := AgentRunRequest{
		AgentID: "a1", AgentSlug: "viktor",
		CrewID: "crew-1", ChatID: "chat-1",
	}
	reqEva := AgentRunRequest{
		AgentID: "a2", AgentSlug: "eva",
		CrewID: "crew-1", ChatID: "chat-2",
	}

	envViktor := BuildEnvVars(reqViktor, nil)
	envEva := BuildEnvVars(reqEva, nil)

	getHome := func(env []string) string {
		for _, e := range env {
			if len(e) > 5 && e[:5] == "HOME=" {
				return e[5:]
			}
		}
		return ""
	}

	homeViktor := getHome(envViktor)
	homeEva := getHome(envEva)

	if homeViktor == homeEva {
		t.Fatalf("agents must have different HOME dirs, both got %q", homeViktor)
	}
	if homeViktor != "/crew/agents/viktor" {
		t.Errorf("expected /crew/agents/viktor, got %q", homeViktor)
	}
	if homeEva != "/crew/agents/eva" {
		t.Errorf("expected /crew/agents/eva, got %q", homeEva)
	}
}

func TestBuildEnvVarsSidecar_SecretCredentials_HandledByKeeper(t *testing.T) {
	// SECRET credentials must NOT be injected as env vars — agents must use the Keeper API.
	// API_KEY credentials are also not injected (sidecar handles them via HTTP proxy).
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "tomas",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
		Credentials: []Credential{
			{ID: "c1", Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat-token"},
			{ID: "c2", Type: "SECRET", EnvVarName: "GMAIL_PASSWORD", PlainValue: "my-app-password"},
			{ID: "c3", Type: "SECRET", EnvVarName: "GOOGLE_ACCOUNT", PlainValue: "user@gmail.com"},
			{ID: "c4", Type: "API_KEY", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-openai-real"},
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

	// SECRET credentials must NOT be injected — access via Keeper API only
	if _, ok := envMap["GMAIL_PASSWORD"]; ok {
		t.Error("SECRET credential GMAIL_PASSWORD must NOT be in sidecar env vars")
	}
	if _, ok := envMap["GOOGLE_ACCOUNT"]; ok {
		t.Error("SECRET credential GOOGLE_ACCOUNT must NOT be in sidecar env vars")
	}
	// API_KEY credentials must NOT be injected with real value (sidecar handles them)
	if v, ok := envMap["OPENAI_API_KEY"]; ok && v == "sk-openai-real" {
		t.Error("API_KEY credential must NOT have real value in sidecar env")
	}
}

func TestBuildEnvVarsSidecar_KeeperDisabled_InjectsSecrets(t *testing.T) {
	// When Keeper is disabled, SECRET credentials should be injected as env vars (legacy mode).
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "tomas",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
		Credentials: []Credential{
			{ID: "c1", Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat-token"},
			{ID: "c2", Type: "SECRET", EnvVarName: "GMAIL_PASSWORD", PlainValue: "my-app-password"},
			{ID: "c3", Type: "SECRET", EnvVarName: "GOOGLE_ACCOUNT", PlainValue: "user@gmail.com"},
			{ID: "c4", Type: "API_KEY", EnvVarName: "OPENAI_API_KEY", PlainValue: "sk-openai-real"},
		},
	}
	env := BuildEnvVarsSidecar(req, false)

	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// SECRET credentials MUST be injected when Keeper is disabled
	if v, ok := envMap["GMAIL_PASSWORD"]; !ok || v != "my-app-password" {
		t.Errorf("SECRET credential GMAIL_PASSWORD should be injected when Keeper disabled, got %q", v)
	}
	if v, ok := envMap["GOOGLE_ACCOUNT"]; !ok || v != "user@gmail.com" {
		t.Errorf("SECRET credential GOOGLE_ACCOUNT should be injected when Keeper disabled, got %q", v)
	}
}

func TestSystemPreambleContainsFilesystem(t *testing.T) {
	if !strings.Contains(crewshipSystemPreamble, "FILESYSTEM") {
		t.Error("preamble should contain FILESYSTEM section")
	}
	if !strings.Contains(crewshipSystemPreamble, "/crew/shared") {
		t.Error("preamble should mention /crew/shared")
	}
	if !strings.Contains(crewshipSystemPreamble, "/crew/agents/") {
		t.Error("preamble should mention per-agent HOME at /crew/agents/")
	}
}

func TestHandleStreamJSONLine_MixedContentBlocks(t *testing.T) {
	// With --include-partial-messages, text and thinking are streamed via
	// stream_event deltas and skipped in the assistant message. Only tool_use
	// blocks are emitted from the final assistant message.
	line := `{"type":"assistant","content":[` +
		`{"type":"thinking","thinking":"I need to read the file first"},` +
		`{"type":"tool_use","id":"toolu_abc","name":"Read","input":{"file_path":"test.go"}},` +
		`{"type":"text","text":"Based on my analysis..."}` +
		`]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine([]byte(line), func(e AgentEvent) { events = append(events, e) })

	// Only tool_use is emitted; thinking and text are skipped (already streamed via deltas)
	if len(events) != 1 {
		t.Fatalf("expected 1 event (tool_call only), got %d: %+v", len(events), events)
	}
	if events[0].Type != "tool_call" {
		t.Errorf("expected tool_call event, got %q", events[0].Type)
	}
	if events[0].Content != "Read" {
		t.Errorf("expected tool name 'Read', got %q", events[0].Content)
	}
}

func TestBuildEnvVarsSidecar_CLITokenInjection(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "karel",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
		Credentials: []Credential{
			{ID: "c1", Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-real"},
			{ID: "c2", Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "ghp_testtoken123"},
			{ID: "c3", Type: "CLI_TOKEN", EnvVarName: "GITLAB_TOKEN", PlainValue: "glpat-testtoken456"},
		},
	}

	env := BuildEnvVarsSidecar(req, false)

	// CLI_TOKEN credentials must be injected as direct env vars
	foundGH := false
	foundGL := false
	for _, e := range env {
		if e == "GH_TOKEN=ghp_testtoken123" {
			foundGH = true
		}
		if e == "GITLAB_TOKEN=glpat-testtoken456" {
			foundGL = true
		}
	}
	if !foundGH {
		t.Error("GH_TOKEN not found in sidecar env vars")
	}
	if !foundGL {
		t.Error("GITLAB_TOKEN not found in sidecar env vars")
	}

	// API_KEY credentials must NOT be injected as direct env vars (sidecar proxy handles them)
	for _, e := range env {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY=sk-ant-real") {
			t.Error("real ANTHROPIC_API_KEY should not be in sidecar env vars")
		}
	}
}

func TestBuildEnvVarsSidecar_CLITokenNotDuplicated(t *testing.T) {
	req := AgentRunRequest{
		AgentID:   "a1",
		AgentSlug: "tomas",
		CrewID:    "crew-1",
		ChatID:    "chat-1",
		Credentials: []Credential{
			{ID: "c1", Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "ghp_abc"},
		},
	}

	env := BuildEnvVarsSidecar(req, true)

	count := 0
	for _, e := range env {
		if strings.HasPrefix(e, "GH_TOKEN=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected GH_TOKEN to appear exactly once, got %d", count)
	}
}

func TestDefaultEnvVarForProvider(t *testing.T) {
	tests := []struct {
		provider string
		expected string
	}{
		{"GITHUB", "GH_TOKEN"},
		{"GITLAB", "GITLAB_TOKEN"},
		{"VERCEL", "VERCEL_TOKEN"},
		{"AWS", "AWS_ACCESS_KEY_ID"},
		{"KUBERNETES", "KUBECONFIG"},
		{"CUSTOM_CLI", ""},
		{"UNKNOWN", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			result := DefaultEnvVarForProvider(tt.provider)
			if result != tt.expected {
				t.Errorf("DefaultEnvVarForProvider(%q) = %q, want %q", tt.provider, result, tt.expected)
			}
		})
	}
}

func TestPreRunInstallPackages_InvalidName(t *testing.T) {
	err := PreRunInstallPackages(context.TODO(), nil, "container-1", []string{"gh;rm -rf /"}, slog.Default())
	if err == nil {
		t.Error("expected error for invalid package name with semicolon")
	}
}

func TestPreRunInstallPackages_EmptyList(t *testing.T) {
	err := PreRunInstallPackages(context.TODO(), nil, "container-1", nil, slog.Default())
	if err != nil {
		t.Errorf("expected nil error for empty package list, got %v", err)
	}
}

func TestWriteCredentialFiles_NoCredentials(t *testing.T) {
	err := writeCredentialFiles(context.TODO(), nil, "ctr-1", "agent-a", nil, "/secrets/agent-a", "/secrets/shared", slog.Default())
	if err != nil {
		t.Errorf("expected nil error for empty creds, got %v", err)
	}
}

func TestWriteCredentialFiles_SkipsAPIKeys(t *testing.T) {
	// API_KEY and AI_CLI_TOKEN credentials should not be written as files
	creds := []Credential{
		{ID: "c1", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-123", Type: "API_KEY"},
		{ID: "c2", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "sk-ant-oat-123", Type: "AI_CLI_TOKEN"},
	}
	err := writeCredentialFiles(context.TODO(), nil, "ctr-1", "agent-a", creds, "/secrets/agent-a", "/secrets/shared", slog.Default())
	if err != nil {
		t.Errorf("expected nil for API-only creds, got %v", err)
	}
}

func TestWriteCredentialFiles_SkipsEmptyValues(t *testing.T) {
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "", Type: "CLI_TOKEN"},
		{ID: "c2", EnvVarName: "", PlainValue: "some-val", Type: "SECRET"},
	}
	err := writeCredentialFiles(context.TODO(), nil, "ctr-1", "agent-a", creds, "/secrets/agent-a", "/secrets/shared", slog.Default())
	if err != nil {
		t.Errorf("expected nil for creds with empty name/value, got %v", err)
	}
}

func TestBuildMCPConfig_HTTP(t *testing.T) {
	servers := []MCPServerConfig{
		{
			Name: "sentry", Transport: "http", Endpoint: "https://mcp.sentry.dev/mcp",
			Credential: &MCPCredential{PlainValue: "token123", Type: "bearer"},
		},
	}
	result, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"mcpServers"`) {
		t.Error("missing mcpServers wrapper")
	}
	if !strings.Contains(result, `"sentry"`) {
		t.Error("missing sentry key")
	}
	if !strings.Contains(result, `"type":"http"`) {
		t.Error("missing type")
	}
	if !strings.Contains(result, `"Bearer token123"`) {
		t.Error("missing bearer credential in headers")
	}
}

func TestBuildMCPConfig_Stdio(t *testing.T) {
	servers := []MCPServerConfig{
		{
			Name: "github", Transport: "stdio", Command: "npx",
			Args:       []string{"@modelcontextprotocol/server-github"},
			Credential: &MCPCredential{PlainValue: "ghp_xxx", Type: "bearer", Header: "GITHUB_TOKEN"},
		},
	}
	result, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"command":"npx"`) {
		t.Error("missing command")
	}
	if !strings.Contains(result, `"GITHUB_TOKEN"`) {
		t.Error("missing env var name")
	}
	if !strings.Contains(result, `"ghp_xxx"`) {
		t.Error("missing credential value in env")
	}
}

func TestBuildMCPConfig_Empty(t *testing.T) {
	result, err := buildMCPConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestBuildMCPConfig_APIKey(t *testing.T) {
	servers := []MCPServerConfig{
		{
			Name: "custom", Transport: "streamable-http", Endpoint: "https://api.example.com/mcp",
			Credential: &MCPCredential{PlainValue: "key123", Type: "api_key", Header: "X-Custom-Key"},
		},
	}
	result, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"X-Custom-Key"`) {
		t.Error("missing custom header")
	}
	if !strings.Contains(result, `"key123"`) {
		t.Error("missing api key value")
	}
}

func TestBuildMCPConfig_Mixed(t *testing.T) {
	servers := []MCPServerConfig{
		{
			Name: "github", Transport: "stdio", Command: "npx",
			Args: []string{"-y", "@modelcontextprotocol/server-github"},
			Env:  map[string]string{"GITHUB_TOKEN": "ghp_test"},
		},
		{
			Name: "sentry", Transport: "streamable-http", Endpoint: "https://mcp.sentry.dev/mcp",
			Credential: &MCPCredential{PlainValue: "sntrys_xxx", Type: "bearer"},
		},
		{
			Name: "no-command", Transport: "stdio", Command: "",
		},
	}
	result, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatal(err)
	}
	// Should include both stdio and HTTP servers
	if !strings.Contains(result, `"github"`) {
		t.Error("missing stdio server 'github'")
	}
	if !strings.Contains(result, `"sentry"`) {
		t.Error("missing http server 'sentry'")
	}
	// Stdio server without command should be skipped
	if strings.Contains(result, `"no-command"`) {
		t.Error("server with empty command should be skipped")
	}
	// Verify stdio has env vars
	if !strings.Contains(result, `"GITHUB_TOKEN":"ghp_test"`) {
		t.Error("missing env var in stdio server")
	}
	// Verify HTTP has auth header
	if !strings.Contains(result, `"Bearer sntrys_xxx"`) {
		t.Error("missing bearer token in http server")
	}
}

func TestMcpStdioDomains(t *testing.T) {
	servers := []MCPServerConfig{
		{Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}},
		{Transport: "stdio", Command: "npx", Args: []string{"-y", "@anthropic-ai/brave-search-mcp"}},
		{Transport: "stdio", Command: "npx", Args: []string{"-y", "@anthropic-ai/brave-search-mcp@latest"}}, // versioned
		{Transport: "stdio", Command: "npx", Args: []string{"-y", "linear-mcp@^2.0.0"}},                     // unscoped versioned
		{Transport: "streamable-http", Endpoint: "https://mcp.sentry.dev/mcp"},                              // should be ignored
		{Transport: "stdio", Command: "npx", Args: []string{"-y", "unknown-package"}},                       // no match
		{Transport: "stdio", Command: "python3", Args: []string{"@stripe/mcp"}},                             // non-launcher, should be ignored
	}
	domains := mcpStdioDomains(servers)
	found := make(map[string]bool)
	for _, d := range domains {
		found[d] = true
	}
	if !found["api.github.com"] {
		t.Error("missing api.github.com")
	}
	if !found["api.search.brave.com"] {
		t.Error("missing api.search.brave.com")
	}
	if !found["api.linear.app"] {
		t.Error("missing api.linear.app from versioned unscoped package")
	}
	if found["sentry.io"] {
		t.Error("HTTP server domains should not be included")
	}
	// Verify output is sorted
	for i := 1; i < len(domains); i++ {
		if domains[i] < domains[i-1] {
			t.Errorf("output not sorted: %v", domains)
			break
		}
	}
}

func TestNormalizeNPMPackage(t *testing.T) {
	cases := []struct{ in, want string }{
		{"@anthropic-ai/brave-search-mcp", "@anthropic-ai/brave-search-mcp"},
		{"@anthropic-ai/brave-search-mcp@latest", "@anthropic-ai/brave-search-mcp"},
		{"@modelcontextprotocol/server-github@^2.0.0", "@modelcontextprotocol/server-github"},
		{"linear-mcp@1.2.3", "linear-mcp"},
		{"linear-mcp", "linear-mcp"},
		{"-y", "-y"},
	}
	for _, tc := range cases {
		got := normalizeNPMPackage(tc.in)
		if got != tc.want {
			t.Errorf("normalizeNPMPackage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildCLICommand_WithMCP(t *testing.T) {
	req := AgentRunRequest{
		CLIAdapter:  "CLAUDE_CODE",
		AgentSlug:   "test-agent",
		UserMessage: "hello",
		MCPServers: []MCPServerConfig{
			{Name: "test", Transport: "http", Endpoint: "https://example.com/mcp"},
		},
	}
	cmd := BuildCLICommand(req)
	found := false
	for i, arg := range cmd {
		if arg == "--mcp-config" && i+1 < len(cmd) {
			found = true
			if !strings.Contains(cmd[i+1], ".mcp.json") {
				t.Errorf("expected .mcp.json file path, got: %s", cmd[i+1])
			}
		}
	}
	if !found {
		t.Error("--mcp-config flag not found in command")
	}
}

func TestBuildCLICommand_WithoutMCP(t *testing.T) {
	req := AgentRunRequest{
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "hello",
	}
	cmd := BuildCLICommand(req)
	for _, arg := range cmd {
		if arg == "--mcp-config" {
			t.Error("--mcp-config should not be present when no MCP servers")
		}
	}
}

func TestBuildCLICommand_WithRawMCPConfig(t *testing.T) {
	req := AgentRunRequest{
		CLIAdapter:        "CLAUDE_CODE",
		AgentSlug:         "test-agent",
		UserMessage:       "hello",
		CrewMCPConfigJSON: `{"mcpServers":{"github":{"command":"npx"}}}`,
	}
	cmd := BuildCLICommand(req)
	found := false
	for _, arg := range cmd {
		if arg == "--mcp-config" {
			found = true
		}
	}
	if !found {
		t.Error("--mcp-config should be present when CrewMCPConfigJSON is set")
	}
}

func TestMergeMCPConfigs_CrewOnly(t *testing.T) {
	crew := `{"mcpServers":{"github":{"command":"npx","args":["-y","@mcp/server-github"]}}}`
	result, err := mergeMCPConfigs(crew, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "github") {
		t.Error("expected github in merged config")
	}
}

func TestMergeMCPConfigs_AgentOverridesCrew(t *testing.T) {
	crew := `{"mcpServers":{"github":{"command":"npx","args":["old"]}}}`
	agent := `{"mcpServers":{"github":{"command":"npx","args":["new"]},"jira":{"type":"http","url":"https://jira.example.com"}}}`
	result, err := mergeMCPConfigs(crew, agent)
	if err != nil {
		t.Fatal(err)
	}
	// Agent's github should override crew's
	if !strings.Contains(result, `"new"`) {
		t.Error("expected agent override for github")
	}
	if strings.Contains(result, `"old"`) {
		t.Error("crew github args should be overridden")
	}
	// Jira should be added
	if !strings.Contains(result, "jira") {
		t.Error("expected jira from agent config")
	}
}

func TestMergeMCPConfigs_BothEmpty(t *testing.T) {
	result, err := mergeMCPConfigs("", "")
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty, got %s", result)
	}
}

func TestMergeMCPConfigs_InvalidJSON(t *testing.T) {
	_, err := mergeMCPConfigs("{invalid", "")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
