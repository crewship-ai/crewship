package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolSchemas_AllFourPresent is the foundational TDD assertion for
// PR-A F1: every CLI adapter (CLAUDE_CODE, CODEX_CLI, GEMINI_CLI,
// OPENCODE, CURSOR_CLI, FACTORY_DROID) registers the SAME four tools
// against the dispatcher. If a schema goes missing the adapter
// wiring breaks silently because the model never sees the tool —
// lock the contract here.
func TestToolSchemas_AllFourPresent(t *testing.T) {
	schemas := ToolSchemas()
	wantNames := []string{
		"memory.read",
		"memory.write",
		"memory.search",
		"memory.append_daily",
	}
	if got := len(schemas); got != len(wantNames) {
		t.Fatalf("expected %d schemas, got %d", len(wantNames), got)
	}
	for _, name := range wantNames {
		s, ok := schemas[name]
		if !ok {
			t.Errorf("missing schema for %q", name)
			continue
		}
		if s.Name != name {
			t.Errorf("schema name mismatch: want %q, got %q", name, s.Name)
		}
		if s.Description == "" {
			t.Errorf("schema %q has empty description (model needs this to know when to call)", name)
		}
		if len(s.InputSchema) == 0 {
			t.Errorf("schema %q has empty InputSchema", name)
		}
		// Each schema's InputSchema must be valid JSON so adapters
		// can pass it verbatim to the model.
		var raw map[string]any
		if err := json.Unmarshal(s.InputSchema, &raw); err != nil {
			t.Errorf("schema %q InputSchema is not valid JSON: %v", name, err)
		}
	}
}

// TestToolSchemas_ReadDeclaresTierEnum locks the tier enum on
// memory.read so the model can't invent invalid tiers and the
// dispatcher's tier validation has a known surface.
func TestToolSchemas_ReadDeclaresTierEnum(t *testing.T) {
	s := ToolSchemas()["memory.read"]
	var raw map[string]any
	_ = json.Unmarshal(s.InputSchema, &raw)
	props, _ := raw["properties"].(map[string]any)
	tier, _ := props["tier"].(map[string]any)
	enum, _ := tier["enum"].([]any)
	wantTiers := map[string]bool{
		"AGENT": false, "CREW": false, "PERSONA": false,
		"pins": false, "daily": false, "peers": false, "lessons": false,
	}
	for _, v := range enum {
		if s, ok := v.(string); ok {
			if _, want := wantTiers[s]; want {
				wantTiers[s] = true
			} else {
				t.Errorf("unexpected tier %q in memory.read enum", s)
			}
		}
	}
	for tier, present := range wantTiers {
		if !present {
			t.Errorf("memory.read tier enum missing %q", tier)
		}
	}
}

// TestToolSchemas_WriteDeclaresModeEnum locks the mode enum on
// memory.write — the model must pick replace or append; never
// silently default to a destructive write.
func TestToolSchemas_WriteDeclaresModeEnum(t *testing.T) {
	s := ToolSchemas()["memory.write"]
	var raw map[string]any
	_ = json.Unmarshal(s.InputSchema, &raw)
	props, _ := raw["properties"].(map[string]any)
	mode, _ := props["mode"].(map[string]any)
	enum, _ := mode["enum"].([]any)
	got := map[string]bool{}
	for _, v := range enum {
		if s, ok := v.(string); ok {
			got[s] = true
		}
	}
	if !got["replace"] || !got["append"] {
		t.Errorf("memory.write mode enum must include replace + append; got %v", enum)
	}
	if len(got) != 2 {
		t.Errorf("memory.write mode enum should contain ONLY replace + append; got %v", enum)
	}
}

// TestDispatch_UnknownTool_ReturnsErrorResult lets the model recover
// without crashing the run when it hallucinates a tool name.
// Returning a ToolResult with IsError=true (instead of an error)
// matches the Anthropic / OpenAI tool_result convention: the model
// gets feedback and tries again.
func TestDispatch_UnknownTool_ReturnsErrorResult(t *testing.T) {
	d := NewDispatcher(testAgentCtx(t))
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.bogus",
		Args: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("dispatcher should not return Go error for unknown tool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError=true for unknown tool, got %+v", res)
	}
	if !strings.Contains(res.Content, "unknown tool") {
		t.Errorf("error content should mention 'unknown tool', got %q", res.Content)
	}
}

// TestDispatch_Read_ReturnsFileContents covers the happy path for
// memory.read on a tier that maps to a file (AGENT.md / CREW.md /
// PERSONA.md / pins).
func TestDispatch_Read_ReturnsFileContents(t *testing.T) {
	ctx := testAgentCtx(t)
	// Seed AGENT.md
	if err := os.WriteFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"), []byte("hello from agent memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(ctx)
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.read",
		Args: json.RawMessage(`{"tier":"AGENT"}`),
	})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hello from agent memory") {
		t.Errorf("expected file contents in result, got: %q", res.Content)
	}
}

// TestDispatch_Read_MissingFile_NotAnError — a missing memory file is
// the normal initial state of a fresh agent. Returning IsError=true
// would make every fresh agent's first read look like a fault.
func TestDispatch_Read_MissingFile_NotAnError(t *testing.T) {
	d := NewDispatcher(testAgentCtx(t))
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.read",
		Args: json.RawMessage(`{"tier":"AGENT"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("missing file should not be IsError; got %q", res.Content)
	}
	// Content can be empty string but must not be the literal word
	// "error" or "not found" leaking implementation detail.
	lower := strings.ToLower(res.Content)
	if strings.Contains(lower, "error") || strings.Contains(lower, "not found") {
		t.Errorf("missing-file path should be quiet, got: %q", res.Content)
	}
}

// TestDispatch_Read_RejectsInvalidTier guards the tier enum at the
// boundary. Schema validation lives at the adapter, but the
// dispatcher must defend against a hallucinated tier slipping past.
func TestDispatch_Read_RejectsInvalidTier(t *testing.T) {
	d := NewDispatcher(testAgentCtx(t))
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.read",
		Args: json.RawMessage(`{"tier":"BOGUS"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("invalid tier should yield IsError=true")
	}
	if !strings.Contains(res.Content, "tier") {
		t.Errorf("error content should mention tier, got %q", res.Content)
	}
}

// TestDispatch_Write_HappyPath persists to disk and reports new size.
func TestDispatch_Write_HappyPath(t *testing.T) {
	ctx := testAgentCtx(t)
	d := NewDispatcher(ctx)
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"first fact\n","mode":"replace"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("expected success, got error: %s", res.Content)
	}

	data, err := os.ReadFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first fact\n" {
		t.Errorf("file contents wrong: %q", string(data))
	}
}

// TestDispatch_Write_AppendMode appends to existing file rather than
// overwriting — required for daily-style additive logs.
func TestDispatch_Write_AppendMode(t *testing.T) {
	ctx := testAgentCtx(t)
	if err := os.WriteFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(ctx)
	_, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"second\n","mode":"append"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"))
	if string(data) != "first\nsecond\n" {
		t.Errorf("append produced wrong file: %q", string(data))
	}
}

// TestDispatch_Write_AppendOverCap_HardError pins the "cap forces
// curation" contract. When an append would exceed the tier cap, the
// writer rejects with a structured error including current size + cap
// so the agent can self-prune and retry.
func TestDispatch_Write_AppendOverCap_HardError(t *testing.T) {
	ctx := testAgentCtx(t)
	// Pre-fill AGENT.md to near the 4000 B cap
	pre := strings.Repeat("x", 3900) + "\n"
	if err := os.WriteFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(ctx)
	overflow := strings.Repeat("y", 200) // 200 B push past 4000 cap
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"` + overflow + `","mode":"append"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("over-cap append must return IsError=true; got %+v", res)
	}
	if !strings.Contains(res.Content, "cap") {
		t.Errorf("error message should mention cap, got %q", res.Content)
	}
}

// TestDispatch_Write_SoftWarningAt80Pct delivers a non-blocking
// warning when usage crosses 80% of cap so the agent can prune
// before the hard error bites.
func TestDispatch_Write_SoftWarningAt80Pct(t *testing.T) {
	ctx := testAgentCtx(t)
	// Pre-fill AGENT.md to ~70% of cap
	pre := strings.Repeat("x", 2800)
	if err := os.WriteFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(ctx)
	// Append enough to push past 80% (cap 4000 → threshold 3200)
	add := strings.Repeat("y", 500) // 2800 + 500 = 3300 > 3200
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"` + add + `","mode":"append"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("write under cap should succeed; got error: %s", res.Content)
	}
	if !strings.Contains(strings.ToLower(res.Content), "warning") &&
		!strings.Contains(strings.ToLower(res.Content), "approaching") {
		t.Errorf("expected warning at 80%% cap, got: %q", res.Content)
	}
}

// TestDispatch_Search_LimitClampedTo20 prevents the model from
// requesting an unbounded result set that would dump the entire
// memory corpus back into context.
func TestDispatch_Search_LimitClampedTo20(t *testing.T) {
	d := NewDispatcher(testAgentCtx(t))
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.search",
		Args: json.RawMessage(`{"q":"anything","limit":100}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Search returning zero hits is fine (empty test fixture); we
	// just want to confirm that 100 didn't trip a validation error
	// and the clamp happened silently. IsError=true would mean the
	// dispatcher rejected the clamp boundary as illegal — wrong.
	if res.IsError {
		t.Errorf("search limit=100 should be clamped, not rejected: %s", res.Content)
	}
}

// TestDispatch_AppendDaily_CreatesFileWithTimestamp writes to the
// today-stamped daily log file.
func TestDispatch_AppendDaily_CreatesFileWithTimestamp(t *testing.T) {
	ctx := testAgentCtx(t)
	d := NewDispatcher(ctx)
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.append_daily",
		Args: json.RawMessage(`{"entry":"first daily entry"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("append_daily failed: %s", res.Content)
	}

	// File should land at daily/YYYY-MM-DD.md under the agent dir.
	dailyDir := filepath.Join(ctx.AgentMemoryDir, "daily")
	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		t.Fatalf("daily dir not created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 daily file, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ".md") {
		t.Errorf("daily file must be .md, got %q", entries[0].Name())
	}
}

// testAgentCtx builds a self-contained AgentContext on a temp dir.
func testAgentCtx(t *testing.T) AgentContext {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent", ".memory")
	crewDir := filepath.Join(root, "crew", "shared", ".memory")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(crewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return AgentContext{
		AgentID:        "agent-1",
		CrewID:         "crew-1",
		WorkspaceID:    "ws-1",
		AgentMemoryDir: agentDir,
		CrewMemoryDir:  crewDir,
	}
}
