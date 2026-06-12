package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// journal.go — truncateForPreview, broadcast, mergePayload, intToA, and
// the WS-push side of every emit* helper (the journal-Emit side is
// already covered by the executor tests, which pass ws=nil).
// ---------------------------------------------------------------------------

// captureWS records every BroadcastWorkspace call so tests can assert
// the event type + payload enrichment the broadcast helper applies.
type captureWS struct {
	mu     sync.Mutex
	events []struct {
		workspaceID string
		eventType   string
		payload     map[string]any
	}
}

func (c *captureWS) BroadcastWorkspace(workspaceID, eventType string, payload any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, _ := payload.(map[string]any)
	c.events = append(c.events, struct {
		workspaceID string
		eventType   string
		payload     map[string]any
	}{workspaceID, eventType, m})
}

func (c *captureWS) types() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.eventType)
	}
	return out
}

func TestTruncateForPreview(t *testing.T) {
	t.Parallel()

	// Short string passes through untouched.
	if got := truncateForPreview("short"); got != "short" {
		t.Errorf("short: got %q", got)
	}

	// Exactly previewLen passes through.
	exact := strings.Repeat("a", previewLen)
	if got := truncateForPreview(exact); got != exact {
		t.Errorf("exact-length string must not be truncated")
	}

	// Long ASCII truncates at previewLen with marker.
	long := strings.Repeat("a", previewLen+100)
	got := truncateForPreview(long)
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("expected truncation marker, got tail %q", got[len(got)-20:])
	}
	if len(got) != previewLen+len("...(truncated)") {
		t.Errorf("ASCII cut should land exactly at previewLen, got %d", len(got))
	}

	// Multi-byte boundary: place a 3-byte rune straddling the cut so the
	// walk-back loop has to step over continuation bytes.
	prefix := strings.Repeat("a", previewLen-1)
	multi := prefix + "世界world" // previewLen-1 ASCII + 3-byte rune at the cut
	got = truncateForPreview(multi)
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("expected truncation marker on multibyte input")
	}
	body := strings.TrimSuffix(got, "...(truncated)")
	if body != prefix {
		t.Errorf("expected cut to walk back to rune boundary (len %d), got len %d", len(prefix), len(body))
	}
	// Result must be valid UTF-8 (no split rune).
	for i := 0; i < len(body); i++ {
		if body[i]&0xc0 == 0x80 && i == 0 {
			t.Errorf("body starts with continuation byte")
		}
	}
}

func TestMergePayload_SkipsNonStringKeys(t *testing.T) {
	t.Parallel()
	base := map[string]any{"a": 1}
	out := mergePayload(base, "b", 2, 42, "ignored-value", "c", 3, "dangling")
	if out["a"] != 1 || out["b"] != 2 || out["c"] != 3 {
		t.Errorf("merge lost a pair: %v", out)
	}
	if len(out) != 3 {
		t.Errorf("non-string key or dangling tail leaked into payload: %v", out)
	}
	// base must not be mutated
	if len(base) != 1 {
		t.Errorf("mergePayload mutated the base map: %v", base)
	}
}

func TestIntToA(t *testing.T) {
	t.Parallel()
	cases := map[int]string{
		0:     "0",
		7:     "7",
		42:    "42",
		-13:   "-13",
		10000: "10000",
	}
	for in, want := range cases {
		if got := intToA(in); got != want {
			t.Errorf("intToA(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBroadcast_NilGuards(t *testing.T) {
	t.Parallel()

	// nil receiver: must not panic.
	var nilCtx *pipelineEmitContext
	nilCtx.broadcast("x", nil)

	// nil ws: no-op.
	c := &pipelineEmitContext{emitter: nopEmitter{}, workspaceID: "ws"}
	c.broadcast("x", nil)

	// empty workspace: no-op even with ws wired.
	ws := &captureWS{}
	c2 := &pipelineEmitContext{emitter: nopEmitter{}, ws: ws}
	c2.broadcast("x", nil)
	if len(ws.types()) != 0 {
		t.Errorf("broadcast with empty workspaceID must not push, got %v", ws.types())
	}
}

func TestBroadcast_EnrichesPayload(t *testing.T) {
	t.Parallel()
	ws := &captureWS{}
	c := &pipelineEmitContext{
		emitter:      nopEmitter{},
		ws:           ws,
		workspaceID:  "ws1",
		pipelineID:   "pln_1",
		pipelineSlug: "daily-report",
		runID:        "run_1",
	}
	// nil payload becomes a fresh map with routing keys stamped.
	c.broadcast("pipeline.run.started", nil)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if len(ws.events) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(ws.events))
	}
	e := ws.events[0]
	if e.workspaceID != "ws1" || e.eventType != "pipeline.run.started" {
		t.Errorf("wrong routing: %+v", e)
	}
	if e.payload["pipeline_id"] != "pln_1" || e.payload["pipeline_slug"] != "daily-report" || e.payload["run_id"] != "run_1" {
		t.Errorf("payload missing routing keys: %v", e.payload)
	}
}

// TestEmitHelpers_NilReceiverIsSafe pins the `if c == nil` guard on
// every emit helper — the executor calls them through a pointer that
// can legitimately be nil on early-exit paths.
func TestEmitHelpers_NilReceiverIsSafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var c *pipelineEmitContext
	step := Step{ID: "s1", Type: StepAgentRun}

	c.emitRunStarted(ctx, ModeRun, "x", 1)
	c.emitRunResumed(ctx, ModeRun, 1, 2)
	c.emitStepStarted(ctx, step, 0, AdapterModel{})
	c.emitStepCompleted(ctx, step, "out", 1, 0.1)
	c.emitStepFailed(ctx, step, "class", "msg")
	c.emitStepSkipped(ctx, step, "cond")
	c.emitStepRetry(ctx, step, 1, "err", time.Second)
	c.emitValidationFailed(ctx, step, "reason", OnFailAbort)
	c.emitRunCompleted(ctx, 10, 0.5)
	c.emitRunFailed(ctx, "s1", "boom")
}

// TestEmitHelpers_BroadcastEveryEvent drives every emit helper with a
// wired WS broadcaster and asserts each one pushes its event type with
// the routing keys present — this is the live-update contract the
// Graph view depends on.
func TestEmitHelpers_BroadcastEveryEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ws := &captureWS{}
	emitted := &captureEmitter{}
	c := &pipelineEmitContext{
		emitter:         emitted,
		ws:              ws,
		workspaceID:     "ws1",
		authorCrewID:    "crew_a",
		invokingCrewID:  "crew_b",
		invokingAgentID: "agent_x",
		pipelineID:      "pln_1",
		pipelineSlug:    "report",
		runID:           "run_1",
	}
	step := Step{ID: "s1", Type: StepAgentRun}

	c.emitRunStarted(ctx, ModeRun, strings.Repeat("i", previewLen+50), 3)
	c.emitRunResumed(ctx, ModeRun, 2, 3)
	c.emitStepStarted(ctx, step, 0, AdapterModel{Adapter: "claude", Model: "haiku"})
	c.emitStepCompleted(ctx, step, "output", 12, 0.01)
	c.emitStepFailed(ctx, step, "agent_run_error", "boom")
	c.emitStepSkipped(ctx, step, "{{ inputs.go }}")
	c.emitStepRetry(ctx, step, 2, "rate limit", 800*time.Millisecond)
	c.emitValidationFailed(ctx, step, "too short", OnFailEscalateTier)
	c.emitRunCompleted(ctx, 100, 0.05)
	c.emitRunFailed(ctx, "s1", "fatal")

	want := []string{
		"pipeline.run.started",
		"pipeline.run.started", // resumed reuses the started event type
		"pipeline.step.started",
		"pipeline.step.completed",
		"pipeline.step.failed",
		"pipeline.step.skipped",
		"pipeline.step.retry",
		"pipeline.step.validation_failed",
		"pipeline.run.completed",
		"pipeline.run.failed",
	}
	got := ws.types()
	if len(got) != len(want) {
		t.Fatalf("expected %d broadcasts, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("broadcast[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Spot-check payload contents that carry semantic weight.
	ws.mu.Lock()
	defer ws.mu.Unlock()
	resumed := ws.events[1].payload
	if resumed["resumed"] != true {
		t.Errorf("resumed payload missing resumed=true: %v", resumed)
	}
	if resumed["restored_steps"] != 2 {
		t.Errorf("resumed payload restored_steps = %v", resumed["restored_steps"])
	}
	retry := ws.events[6].payload
	if retry["attempt"] != 2 {
		t.Errorf("retry payload attempt = %v", retry["attempt"])
	}
	if retry["sleep_ms"] != int64(800) {
		t.Errorf("retry payload sleep_ms = %v", retry["sleep_ms"])
	}
	skipped := ws.events[5].payload
	if skipped["condition"] != "{{ inputs.go }}" {
		t.Errorf("skipped payload condition = %v", skipped["condition"])
	}

	// And the inputs preview must have been truncated for the started event.
	started := ws.events[0].payload
	preview, _ := started["inputs_preview"].(string)
	if !strings.HasSuffix(preview, "...(truncated)") {
		t.Errorf("run.started inputs_preview not truncated: len=%d", len(preview))
	}

	// Journal side got the same 10 entries.
	if n := len(emitted.typesEmitted()); n != 10 {
		t.Errorf("expected 10 journal entries, got %d", n)
	}
}
