package orchestrator

import (
	"log/slog"
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
