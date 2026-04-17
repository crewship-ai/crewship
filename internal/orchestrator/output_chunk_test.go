package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// chunkRecorder captures JournalEntries so tests can assert that the
// end-of-stream exec.output_chunk emit carries the right bytes and
// truncation flags. Safe for concurrent Emit from multiple goroutines.
type chunkRecorder struct {
	mu      sync.Mutex
	entries []JournalEntry
}

func (r *chunkRecorder) Emit(_ context.Context, e JournalEntry) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	return "id", nil
}

func (r *chunkRecorder) findFirst(kind string) *JournalEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.entries {
		if r.entries[i].Type == kind {
			return &r.entries[i]
		}
	}
	return nil
}

func TestStreamOutput_EmitsOutputChunkSummary(t *testing.T) {
	t.Parallel()

	o := New(nil, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	// Small output — no truncation expected.
	result := &provider.ExecResult{
		ExecID: "exec-a",
		Reader: io.NopCloser(strings.NewReader("hello\nworld\n")),
	}
	req := AgentRunRequest{
		AgentID: "a1", AgentSlug: "sluggy", WorkspaceID: "ws", CrewID: "c1",
		ChatID: "chat", CLIAdapter: "PLAIN", // non-stream-JSON path to keep the test simple
	}

	o.streamOutput(context.Background(), result, req, nil)

	entry := rec.findFirst("exec.output_chunk")
	if entry == nil {
		t.Fatalf("no exec.output_chunk entry emitted; got: %+v", rec.entries)
	}
	// Summary stays stable so Crow's Nest rows render consistently.
	if !strings.Contains(entry.Summary, "sluggy") {
		t.Errorf("summary missing agent slug: %q", entry.Summary)
	}
	payload := entry.Payload
	if payload["exec_id"] != "exec-a" {
		t.Errorf("exec_id = %v", payload["exec_id"])
	}
	tb, ok := payload["total_bytes"].(int64)
	if !ok || tb == 0 {
		t.Errorf("total_bytes missing or zero: %v", payload["total_bytes"])
	}
	if payload["truncated"] != false {
		t.Errorf("expected truncated=false for small output, got %v", payload["truncated"])
	}
	output, _ := payload["output"].(string)
	if !strings.Contains(output, "hello") || !strings.Contains(output, "world") {
		t.Errorf("output capture missing lines: %q", output)
	}
}

func TestStreamOutput_TruncatesLargeOutput(t *testing.T) {
	t.Parallel()

	o := New(nil, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	// Build 32 KB of distinct lines so the 16 KB captureBuf truncates but
	// totalBytes still counts the full payload.
	var sb strings.Builder
	line := strings.Repeat("x", 200) + "\n"
	for i := 0; i < 200; i++ { // 200 * 201 ≈ 40 KB
		sb.WriteString(line)
	}
	full := sb.String()

	result := &provider.ExecResult{
		ExecID: "exec-big",
		Reader: io.NopCloser(strings.NewReader(full)),
	}
	req := AgentRunRequest{
		AgentID: "a1", AgentSlug: "bigboy", WorkspaceID: "ws", CrewID: "c1",
		ChatID: "chat", CLIAdapter: "PLAIN",
	}

	o.streamOutput(context.Background(), result, req, nil)

	entry := rec.findFirst("exec.output_chunk")
	if entry == nil {
		t.Fatalf("no exec.output_chunk entry emitted")
	}
	payload := entry.Payload
	if payload["truncated"] != true {
		t.Errorf("expected truncated=true, got %v", payload["truncated"])
	}
	output, _ := payload["output"].(string)
	if len(output) > 16*1024 {
		t.Errorf("output exceeded cap: %d bytes", len(output))
	}
	tb, _ := payload["total_bytes"].(int64)
	if int(tb) < len(full)/2 {
		t.Errorf("total_bytes should record full volume; got %d for %d-byte input", tb, len(full))
	}
}
