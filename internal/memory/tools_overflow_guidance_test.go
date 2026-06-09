package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDispatch_Write_OverCap_AttachesCurrentEntriesAndGuidance pins PR #6:
// when an append blows the cap, the hard-error result must carry the current
// on-disk file body as metadata["current_entries"] and a metadata["usage"]
// string, and the message must instruct the agent to consolidate then retry
// within the same turn (rather than just "drop older entries before
// retrying"). The store itself stays a pure bounded store — nothing is
// written to disk.
func TestDispatch_Write_OverCap_AttachesCurrentEntriesAndGuidance(t *testing.T) {
	ctx := testAgentCtx(t)
	pre := strings.Repeat("x", 3900) + "\n"
	if err := os.WriteFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(ctx)
	overflow := strings.Repeat("y", 300)
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
	// current_entries == the body that is on disk right now.
	ce, ok := res.Metadata["current_entries"].(string)
	if !ok {
		t.Fatalf("expected metadata.current_entries string, got %T", res.Metadata["current_entries"])
	}
	if ce != pre {
		t.Errorf("current_entries must equal current on-disk body;\nwant %q\ngot  %q", pre, ce)
	}
	// usage string present.
	if _, ok := res.Metadata["usage"].(string); !ok {
		t.Fatalf("expected metadata.usage string, got %T", res.Metadata["usage"])
	}
	// Message tells the agent to consolidate and retry in this turn.
	lc := strings.ToLower(res.Content)
	if !strings.Contains(lc, "consolidate") {
		t.Errorf("message should instruct consolidate, got: %q", res.Content)
	}
	if !strings.Contains(lc, "this turn") {
		t.Errorf("message should instruct retry in this turn, got: %q", res.Content)
	}
	// Store stays pure: nothing written, file unchanged.
	after, _ := os.ReadFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"))
	if string(after) != pre {
		t.Errorf("over-cap write must not mutate the file; file changed")
	}
}

// TestDispatch_Write_OverCap_ReplaceParity confirms replace-mode overflow
// gets the SAME current_entries + usage + guidance treatment as append.
func TestDispatch_Write_OverCap_ReplaceParity(t *testing.T) {
	ctx := testAgentCtx(t)
	pre := strings.Repeat("x", 1000) + "\n"
	if err := os.WriteFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(ctx)
	// A replace whose new body alone exceeds the 4000 B cap.
	big := strings.Repeat("z", 4200)
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"` + big + `","mode":"replace"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("over-cap replace must return IsError=true; got %+v", res)
	}
	ce, ok := res.Metadata["current_entries"].(string)
	if !ok {
		t.Fatalf("replace over-cap should attach current_entries; got %T", res.Metadata["current_entries"])
	}
	if ce != pre {
		t.Errorf("current_entries should be the existing on-disk body on replace;\nwant %q\ngot %q", pre, ce)
	}
	if _, ok := res.Metadata["usage"].(string); !ok {
		t.Errorf("replace over-cap should attach usage string")
	}
	lc := strings.ToLower(res.Content)
	if !strings.Contains(lc, "consolidate") || !strings.Contains(lc, "this turn") {
		t.Errorf("replace over-cap message should instruct consolidate-then-retry-in-this-turn, got: %q", res.Content)
	}
}

// TestDispatch_Write_SoftCap_AttachesCurrentEntriesAndGuidance pins the
// soft-cap branch parity: a successful write that crosses the 80% soft cap
// still succeeds, but its (non-error) result now carries current_entries +
// usage and the warning text steers toward consolidate-then-retry-in-this-turn.
func TestDispatch_Write_SoftCap_AttachesCurrentEntriesAndGuidance(t *testing.T) {
	ctx := testAgentCtx(t)
	pre := strings.Repeat("x", 2800)
	if err := os.WriteFile(filepath.Join(ctx.AgentMemoryDir, "AGENT.md"), []byte(pre), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(ctx)
	add := strings.Repeat("y", 500) // 3300 > 3200 soft threshold, < 4000 cap
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"` + add + `","mode":"append"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("soft-cap write should succeed; got error: %s", res.Content)
	}
	// current_entries == the now-written body (3300 B).
	ce, ok := res.Metadata["current_entries"].(string)
	if !ok {
		t.Fatalf("soft-cap success should attach current_entries; got %T", res.Metadata["current_entries"])
	}
	if ce != pre+add {
		t.Errorf("soft-cap current_entries should be the freshly-written body")
	}
	if _, ok := res.Metadata["usage"].(string); !ok {
		t.Errorf("soft-cap success should attach usage string")
	}
	lc := strings.ToLower(res.Content)
	if !strings.Contains(lc, "consolidate") {
		t.Errorf("soft-cap warning should mention consolidate, got: %q", res.Content)
	}
	if !strings.Contains(lc, "this turn") {
		t.Errorf("soft-cap warning should steer toward retrying in this turn, got: %q", res.Content)
	}
}

// TestDispatch_Write_UnderSoftCap_NoGuidanceMetadata ensures a comfortably
// small write does NOT get the soft-cap consolidation metadata — the
// guidance only fires near the cap.
func TestDispatch_Write_UnderSoftCap_NoGuidanceMetadata(t *testing.T) {
	ctx := testAgentCtx(t)
	d := NewDispatcher(ctx)
	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","content":"short note","mode":"replace"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("small write should succeed: %s", res.Content)
	}
	if _, ok := res.Metadata["current_entries"]; ok {
		t.Errorf("under-soft-cap write must not attach current_entries")
	}
	if strings.Contains(strings.ToLower(res.Content), "consolidate") {
		t.Errorf("under-soft-cap write must not nag about consolidation, got: %q", res.Content)
	}
}
