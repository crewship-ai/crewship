package orchestrator

import (
	"log/slog"
	"strings"
	"testing"
)

func TestHandleStreamJSONLine_TextDelta(t *testing.T) {
	o := New(nil, nil, slog.Default())

	line := `{"type":"stream_event","event":{"type":"delta","delta":{"type":"text_delta","text":"Hello "}}}`

	var events []AgentEvent
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

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
	// Claude Code emits assistant messages with thinking content blocks
	line := `{"type":"assistant","content":[{"type":"thinking","thinking":"Let me analyze this code..."},{"type":"text","text":"Here is my answer"}]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}

	// First event should be thinking
	if events[0].Type != "thinking" {
		t.Errorf("expected thinking event, got %q", events[0].Type)
	}
	if events[0].Content != "Let me analyze this code..." {
		t.Errorf("expected thinking content, got %q", events[0].Content)
	}

	// Second event should be text
	if events[1].Type != "text" {
		t.Errorf("expected text event, got %q", events[1].Type)
	}
	if events[1].Content != "Here is my answer" {
		t.Errorf("expected text content, got %q", events[1].Content)
	}
}

func TestHandleStreamJSONLine_ThinkingDelta(t *testing.T) {
	// Claude Code stream events can include thinking_delta for streaming thinking
	line := `{"type":"stream_event","event":{"type":"delta","delta":{"type":"thinking_delta","thinking":"analyzing..."}}}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

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
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

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
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

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
	o.handleStreamJSONLine(`{"type":"assistant","content":[{"type":"text","text":"hello"}]}`, nil)
}

func TestHandleStreamJSONLine_InvalidJSON(t *testing.T) {
	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine("not json at all", func(e AgentEvent) { events = append(events, e) })

	if len(events) != 1 {
		t.Fatalf("expected 1 fallback event, got %d", len(events))
	}
	if events[0].Type != "text" {
		t.Errorf("expected fallback text event, got %q", events[0].Type)
	}
}

func TestHandleStreamJSONLine_Result(t *testing.T) {
	// "result" messages are intentionally ignored because they duplicate
	// content already delivered via "assistant" content blocks.
	o := New(nil, nil, slog.Default())
	line := `{"type":"result","result":"Final answer text"}`

	var events []AgentEvent
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

	if len(events) != 0 {
		t.Fatalf("expected 0 events (result is skipped to avoid duplication), got %d", len(events))
	}
}

func TestHandleStreamJSONLine_ToolTypeMessage(t *testing.T) {
	// Claude Code emits tool results as top-level "tool" type messages
	line := `{"type":"tool","content":[{"type":"tool_result","tool_use_id":"toolu_abc","text":"file contents here"}]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

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
	env := BuildEnvVarsSidecar(req)

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
	env := BuildEnvVarsSidecar(req)

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
	// A realistic Claude Code response with thinking + tool_use + text
	line := `{"type":"assistant","content":[` +
		`{"type":"thinking","thinking":"I need to read the file first"},` +
		`{"type":"tool_use","id":"toolu_abc","name":"Read","input":{"file_path":"test.go"}},` +
		`{"type":"text","text":"Based on my analysis..."}` +
		`]}`

	o := New(nil, nil, slog.Default())
	var events []AgentEvent
	o.handleStreamJSONLine(line, func(e AgentEvent) { events = append(events, e) })

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}

	types := []string{events[0].Type, events[1].Type, events[2].Type}
	expected := []string{"thinking", "tool_call", "text"}
	for i, exp := range expected {
		if types[i] != exp {
			t.Errorf("event %d: expected %q, got %q", i, exp, types[i])
		}
	}
}
