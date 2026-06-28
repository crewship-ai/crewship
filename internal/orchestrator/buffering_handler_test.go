package orchestrator

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logcollector"
)

// readLogEntries reads back the JSONL log file the OutputBuffer wrote for the
// given crew/agent under base.
func readLogEntries(t *testing.T, base, crewID, agentID string) []logcollector.LogEntry {
	t.Helper()
	path := filepath.Join(base, "crews", crewID, "agents", agentID, "current.jsonl")
	f, err := os.Open(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer f.Close()

	var out []logcollector.LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e logcollector.LogEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("unmarshal log line %q: %v", line, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestNewBufferingHandler_BuffersAccumulatesAndCaptures(t *testing.T) {
	base := t.TempDir()
	w := logcollector.NewWriter(base, slog.New(slog.NewTextHandler(io.Discard, nil)))
	logBuf := logcollector.NewOutputBuffer(w, "crew1", "agent1")

	handler, acc := NewBufferingHandler(BufferingHandlerOpts{
		LogBuf:            logBuf,
		AgentSlug:         "agent1",
		AccumulateText:    true,
		CaptureResultMeta: true,
	})

	ts := time.Now().UTC()
	// "text" events accumulate into acc.Text(); they are streamed events so
	// the buffer aggregates them — newline forces a flush so we can read them.
	handler(AgentEvent{Type: "text", Content: "Hello ", Timestamp: ts})
	handler(AgentEvent{Type: "text", Content: "world\n", Timestamp: ts})
	// A non-streamed event flushes immediately.
	handler(AgentEvent{Type: "tool_call", Content: "ls", Timestamp: ts})
	// "result" carries the run metadata we want captured.
	resultMeta := map[string]any{
		"total_cost_usd": 0.42,
		"usage": map[string]any{
			"input_tokens":  float64(100),
			"output_tokens": float64(50),
		},
	}
	handler(AgentEvent{Type: "result", Content: "", Metadata: resultMeta, Timestamp: ts})

	logBuf.Close()
	w.Close()

	if got, want := acc.Text(), "Hello world\n"; got != want {
		t.Errorf("acc.Text() = %q, want %q", got, want)
	}
	rm := acc.ResultMeta()
	if rm == nil {
		t.Fatalf("acc.ResultMeta() = nil, want captured map")
	}
	if rm["total_cost_usd"] != 0.42 {
		t.Errorf("captured total_cost_usd = %v, want 0.42", rm["total_cost_usd"])
	}

	entries := readLogEntries(t, base, "crew1", "agent1")
	// Expect: aggregated text line, tool_call, result → 3 entries.
	if len(entries) != 3 {
		t.Fatalf("got %d log entries, want 3: %+v", len(entries), entries)
	}
	events := map[string]bool{}
	for _, e := range entries {
		events[e.Event] = true
		if e.Agent != "agent1" {
			t.Errorf("entry.Agent = %q, want agent1", e.Agent)
		}
		if e.Level != "info" {
			t.Errorf("entry.Level = %q, want info", e.Level)
		}
	}
	for _, want := range []string{"text", "tool_call", "result"} {
		if !events[want] {
			t.Errorf("missing log entry for event %q", want)
		}
	}
}

func TestNewBufferingHandler_DisabledOptions(t *testing.T) {
	// With accumulation/capture off and a nil buffer, the handler is a no-op
	// that never panics and leaves the accumulator empty.
	handler, acc := NewBufferingHandler(BufferingHandlerOpts{AgentSlug: "agent1"})
	handler(AgentEvent{Type: "text", Content: "ignored", Timestamp: time.Now()})
	handler(AgentEvent{Type: "result", Content: "", Metadata: map[string]any{"x": 1}, Timestamp: time.Now()})
	if acc.Text() != "" {
		t.Errorf("acc.Text() = %q, want empty", acc.Text())
	}
	if acc.ResultMeta() != nil {
		t.Errorf("acc.ResultMeta() = %v, want nil", acc.ResultMeta())
	}
}

func TestNewBufferingHandler_OnLogError(t *testing.T) {
	// An invalid agent ID makes the underlying Writer.Append fail, which must
	// surface through OnLogError.
	base := t.TempDir()
	w := logcollector.NewWriter(base, slog.New(slog.NewTextHandler(io.Discard, nil)))
	logBuf := logcollector.NewOutputBuffer(w, "crew1", "bad/agent")
	defer logBuf.Close()

	var gotErr error
	handler, _ := NewBufferingHandler(BufferingHandlerOpts{
		LogBuf:     logBuf,
		AgentSlug:  "bad/agent",
		OnLogError: func(err error) { gotErr = err },
	})
	// tool_call is non-streamed → flushes immediately → Append runs now.
	handler(AgentEvent{Type: "tool_call", Content: "x", Timestamp: time.Now()})
	if gotErr == nil {
		t.Fatalf("OnLogError was not invoked for an invalid agent ID")
	}
}

func TestParseResultUsage(t *testing.T) {
	tests := []struct {
		name     string
		meta     any
		wantCost float64
		wantIn   int
		wantOut  int
	}{
		{
			name: "well-formed",
			meta: map[string]any{
				"total_cost_usd": 1.25,
				"usage": map[string]any{
					"input_tokens":  float64(300),
					"output_tokens": float64(120),
				},
			},
			wantCost: 1.25, wantIn: 300, wantOut: 120,
		},
		{
			name:     "missing fields",
			meta:     map[string]any{"num_turns": float64(3)},
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name: "wrong types",
			meta: map[string]any{
				"total_cost_usd": "1.25", // string, not float64
				"usage": map[string]any{
					"input_tokens":  "300",
					"output_tokens": true,
				},
			},
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name:     "usage not a map",
			meta:     map[string]any{"usage": "nope", "total_cost_usd": 0.5},
			wantCost: 0.5, wantIn: 0, wantOut: 0,
		},
		{
			name:     "nil meta",
			meta:     nil,
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name:     "not a map",
			meta:     "not a map",
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
		{
			name:     "typed nil map",
			meta:     map[string]any(nil),
			wantCost: 0, wantIn: 0, wantOut: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cost, in, out := ParseResultUsage(tc.meta)
			if cost != tc.wantCost || in != tc.wantIn || out != tc.wantOut {
				t.Errorf("ParseResultUsage = (%v,%d,%d), want (%v,%d,%d)",
					cost, in, out, tc.wantCost, tc.wantIn, tc.wantOut)
			}
		})
	}
}

func TestMergeResultUsageMeta(t *testing.T) {
	t.Run("copies known keys, preserves dst", func(t *testing.T) {
		dst := map[string]any{"duration_ms": int64(1000)}
		meta := map[string]any{
			"total_cost_usd": 0.9,
			"num_turns":      float64(4),
			"usage":          map[string]any{"input_tokens": float64(10)},
			"model_usage":    map[string]any{"claude": 1},
			"unrelated":      "drop me",
		}
		MergeResultUsageMeta(dst, meta)
		if dst["duration_ms"] != int64(1000) {
			t.Errorf("duration_ms clobbered: %v", dst["duration_ms"])
		}
		for _, k := range []string{"total_cost_usd", "num_turns", "usage", "model_usage"} {
			if _, ok := dst[k]; !ok {
				t.Errorf("missing copied key %q", k)
			}
		}
		if _, ok := dst["unrelated"]; ok {
			t.Errorf("unrelated key should not be copied")
		}
	})

	t.Run("nil meta is a no-op", func(t *testing.T) {
		dst := map[string]any{"duration_ms": int64(5)}
		MergeResultUsageMeta(dst, nil)
		if len(dst) != 1 {
			t.Errorf("dst mutated by nil meta: %+v", dst)
		}
	})

	t.Run("absent keys skipped", func(t *testing.T) {
		dst := map[string]any{}
		MergeResultUsageMeta(dst, map[string]any{"total_cost_usd": 0.1})
		if len(dst) != 1 {
			t.Errorf("expected only present key copied, got %+v", dst)
		}
	})
}
