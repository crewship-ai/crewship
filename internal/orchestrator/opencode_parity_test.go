package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// opencodeTestLogger keeps streamOutput's diagnostics visible at Warn+
// without the Info noise slog.Default() would emit into test output.
func opencodeTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// opencode_parity_test.go locks the #943 production-parity contract for the
// OPENCODE adapter: Paymaster-readable usage, Crow's Nest model surfacing,
// accumulated-text dedup, and a terminal result even when the CLI exits
// before its step_finish envelope (anomalyco/opencode#26855).

const opencodeStepFinishLine = `{"type":"step_finish","sessionID":"ses_parity","part":{"id":"stp_1","reason":"stop","cost":0.0021,"tokens":{"input":1204,"output":58,"reasoning":0,"cache":{"read":0,"write":0}},"providerID":"anthropic","modelID":"claude-sonnet-4-6"}}`

// TestParseOpenCode_StepFinishUsageReadableByPaymaster feeds the terminal
// step_finish envelope and asserts the shared ParseResultUsage consumer —
// the exact function Paymaster sites call — extracts non-zero cost and
// token counts. Pre-#943 the parser emitted `cost_usd` + `tokens`, keys the
// consumer never reads, so every OpenCode run recorded $0.
func TestParseOpenCode_StepFinishUsageReadableByPaymaster(t *testing.T) {
	var results []AgentEvent
	parseOpenCodeStreamJSON([]byte(opencodeStepFinishLine), func(e AgentEvent) {
		if e.Type == "result" {
			results = append(results, e)
		}
	})
	if len(results) != 1 {
		t.Fatalf("want exactly 1 result event, got %d", len(results))
	}
	cost, tokIn, tokOut := ParseResultUsage(results[0].Metadata)
	if cost != 0.0021 {
		t.Errorf("ParseResultUsage cost = %v, want 0.0021 (parser must emit total_cost_usd)", cost)
	}
	if tokIn != 1204 || tokOut != 58 {
		t.Errorf("ParseResultUsage tokens = %d/%d, want 1204/58 (parser must emit usage.input_tokens/output_tokens)", tokIn, tokOut)
	}
	meta := results[0].Metadata.(map[string]interface{})
	if meta["reason"] != "stop" {
		t.Errorf("result metadata missing step_finish reason: %v", meta["reason"])
	}
}

// TestParseOpenCode_ModelSurfacedForCrowsNest asserts the parser emits a
// system bootstrap event carrying the resolved model so the shared
// Accumulator (and through it logResolvedModel / the run record) captures
// it. OpenCode has no init event; the model rides on step_finish metadata,
// so the parser must surface it from there.
func TestParseOpenCode_ModelSurfacedForCrowsNest(t *testing.T) {
	handler, acc := NewBufferingHandler(BufferingHandlerOpts{CaptureResultMeta: true})
	parseOpenCodeStreamJSON([]byte(opencodeStepFinishLine), handler)
	if got := acc.ResolvedModel(); got != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("ResolvedModel = %q, want %q (no system event with model metadata was emitted)", got, "anthropic/claude-sonnet-4-6")
	}
}

// TestParseOpenCode_AccumulatedTextEmitsOnlyNewSuffix: OpenCode `text`
// events carry the part's ACCUMULATED text so far, not deltas. Emitting
// each event verbatim double-appends ("Hello" + "Hello world"). The parser
// must emit only the new suffix per part.
func TestParseOpenCode_AccumulatedTextEmitsOnlyNewSuffix(t *testing.T) {
	var sb strings.Builder
	h := func(e AgentEvent) {
		if e.Type == "text" {
			sb.WriteString(e.Content)
		}
	}
	parseOpenCodeStreamJSON([]byte(`{"type":"text","sessionID":"ses_sfx","part":{"id":"prt_1","text":"Hello"}}`), h)
	parseOpenCodeStreamJSON([]byte(`{"type":"text","sessionID":"ses_sfx","part":{"id":"prt_1","text":"Hello world"}}`), h)
	if sb.String() != "Hello world" {
		t.Fatalf("accumulated text double-appended: %q, want %q", sb.String(), "Hello world")
	}
}

// TestParseOpenCode_DeltaStyleTextStillWorks: if a future upstream switches
// to delta semantics (new text does NOT extend the previous), the parser
// must fall back to emitting the event verbatim rather than dropping it.
func TestParseOpenCode_DeltaStyleTextStillWorks(t *testing.T) {
	var sb strings.Builder
	h := func(e AgentEvent) {
		if e.Type == "text" {
			sb.WriteString(e.Content)
		}
	}
	parseOpenCodeStreamJSON([]byte(`{"type":"text","sessionID":"ses_dlt","part":{"id":"prt_1","text":"Hello"}}`), h)
	parseOpenCodeStreamJSON([]byte(`{"type":"text","sessionID":"ses_dlt","part":{"id":"prt_1","text":" world"}}`), h)
	if sb.String() != "Hello world" {
		t.Fatalf("delta-style fallback broken: %q, want %q", sb.String(), "Hello world")
	}
}

// TestParseOpenCode_DistinctPartsDoNotDedup: dedup state is per part id —
// two different parts with overlapping text must both emit in full.
func TestParseOpenCode_DistinctPartsDoNotDedup(t *testing.T) {
	var sb strings.Builder
	h := func(e AgentEvent) {
		if e.Type == "text" {
			sb.WriteString(e.Content)
		}
	}
	parseOpenCodeStreamJSON([]byte(`{"type":"text","sessionID":"ses_dst","part":{"id":"prt_a","text":"same"}}`), h)
	parseOpenCodeStreamJSON([]byte(`{"type":"text","sessionID":"ses_dst","part":{"id":"prt_b","text":"same"}}`), h)
	if sb.String() != "samesame" {
		t.Fatalf("distinct parts wrongly deduped: %q, want %q", sb.String(), "samesame")
	}
}

// TestParseOpenCode_ReasoningAccumulatedDedup: reasoning parts stream with
// the same accumulated semantics as text parts.
func TestParseOpenCode_ReasoningAccumulatedDedup(t *testing.T) {
	var sb strings.Builder
	h := func(e AgentEvent) {
		if e.Type == "thinking" {
			sb.WriteString(e.Content)
		}
	}
	parseOpenCodeStreamJSON([]byte(`{"type":"reasoning","sessionID":"ses_rsn","part":{"id":"prt_r","text":"step 1"}}`), h)
	parseOpenCodeStreamJSON([]byte(`{"type":"reasoning","sessionID":"ses_rsn","part":{"id":"prt_r","text":"step 1, step 2"}}`), h)
	if sb.String() != "step 1, step 2" {
		t.Fatalf("reasoning double-appended: %q, want %q", sb.String(), "step 1, step 2")
	}
}

// streamOutput-level tests for the missing-step_finish resilience
// (anomalyco/opencode#26855: the CLI can exit before the terminal envelope).

func opencodeStreamReq() AgentRunRequest {
	return AgentRunRequest{
		AgentID: "a1", AgentSlug: "oc", WorkspaceID: "ws", CrewID: "c1",
		ChatID: "chat", CLIAdapter: "OPENCODE",
	}
}

// TestStreamOutput_SynthesizesTerminalResultOnEarlyExit: stream ends after
// text with no step_finish → streamOutput must synthesize a terminal result
// event (non-error, flagged synthetic) so run finalization has an envelope,
// and the streamed text must be preserved.
func TestStreamOutput_SynthesizesTerminalResultOnEarlyExit(t *testing.T) {
	t.Parallel()
	o := New(nil, newMemState(), opencodeTestLogger())
	o.SetJournal(&chunkRecorder{})

	stream := `{"type":"text","sessionID":"ses_eof","part":{"id":"p1","text":"partial answer"}}` + "\n"
	result := &provider.ExecResult{ExecID: "e1", Reader: io.NopCloser(strings.NewReader(stream))}

	var events []AgentEvent
	o.streamOutput(context.Background(), result, opencodeStreamReq(), func(e AgentEvent) { events = append(events, e) })

	var text string
	var results []AgentEvent
	for _, e := range events {
		if e.Type == "text" {
			text += e.Content
		}
		if e.Type == "result" {
			results = append(results, e)
		}
	}
	if text != "partial answer" {
		t.Errorf("streamed text lost: %q", text)
	}
	if len(results) != 1 {
		t.Fatalf("want exactly 1 synthesized result event, got %d", len(results))
	}
	meta := results[0].Metadata.(map[string]interface{})
	if meta["subtype"] != "stream_eof_synthetic" {
		t.Errorf("synthetic result not flagged: subtype = %v", meta["subtype"])
	}
	if meta["is_error"] != false {
		t.Errorf("synthetic result must be non-error, got is_error = %v", meta["is_error"])
	}
}

// TestStreamOutput_NoSyntheticResultWhenStepFinishArrived: a healthy stream
// must produce exactly one result — no synthetic duplicate.
func TestStreamOutput_NoSyntheticResultWhenStepFinishArrived(t *testing.T) {
	t.Parallel()
	o := New(nil, newMemState(), opencodeTestLogger())
	o.SetJournal(&chunkRecorder{})

	stream := `{"type":"text","sessionID":"ses_ok","part":{"id":"p1","text":"hi"}}` + "\n" + opencodeStepFinishLine + "\n"
	result := &provider.ExecResult{ExecID: "e2", Reader: io.NopCloser(strings.NewReader(stream))}

	var results int
	o.streamOutput(context.Background(), result, opencodeStreamReq(), func(e AgentEvent) {
		if e.Type == "result" {
			results++
		}
	})
	if results != 1 {
		t.Fatalf("want exactly 1 result event from step_finish, got %d", results)
	}
}

// TestStreamOutput_NoSyntheticResultAfterErrorEvent: when the CLI surfaced a
// fatal error envelope and exited, the error path owns run finalization — a
// synthetic non-error result would mask the failure.
func TestStreamOutput_NoSyntheticResultAfterErrorEvent(t *testing.T) {
	t.Parallel()
	o := New(nil, newMemState(), opencodeTestLogger())
	o.SetJournal(&chunkRecorder{})

	stream := `{"type":"error","sessionID":"ses_err","error":"provider returned 500"}` + "\n"
	result := &provider.ExecResult{ExecID: "e3", Reader: io.NopCloser(strings.NewReader(stream))}

	var results int
	o.streamOutput(context.Background(), result, opencodeStreamReq(), func(e AgentEvent) {
		if e.Type == "result" {
			results++
		}
	})
	if results != 0 {
		t.Fatalf("error-terminated stream must not synthesize a result, got %d", results)
	}
}
