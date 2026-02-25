package logcollector

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestOutputBuffer_AggregatesTokens(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, testLogger(t))
	defer w.Close()

	buf := NewOutputBuffer(w, "crew1", "agent1")

	tokens := []string{"Hello", " ", "world", "!", " How", " are", " you?"}
	for _, tok := range tokens {
		if err := buf.Append(LogEntry{
			Event:   "output",
			Content: tok,
		}); err != nil {
			t.Fatal(err)
		}
	}
	buf.Close()

	entries, err := readJSONL(filepath.Join(dir, "crews", "crew1", "agents", "agent1", "current.jsonl"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// All tokens should be in a single aggregated entry (no newlines in input)
	if len(entries) != 1 {
		t.Fatalf("expected 1 aggregated entry, got %d", len(entries))
	}
	want := "Hello world! How are you?"
	if entries[0].Content != want {
		t.Errorf("content = %q, want %q", entries[0].Content, want)
	}
}

func TestOutputBuffer_FlushesOnNewline(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, testLogger(t))
	defer w.Close()

	buf := NewOutputBuffer(w, "crew1", "agent1")

	if err := buf.Append(LogEntry{Event: "output", Content: "line one"}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "output", Content: "\n"}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "output", Content: "line two"}); err != nil {
		t.Fatal(err)
	}
	buf.Close()

	entries, err := readJSONL(filepath.Join(dir, "crews", "crew1", "agents", "agent1", "current.jsonl"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (split on newline), got %d", len(entries))
	}
	if entries[0].Content != "line one\n" {
		t.Errorf("entry[0] = %q, want %q", entries[0].Content, "line one\n")
	}
	if entries[1].Content != "line two" {
		t.Errorf("entry[1] = %q, want %q", entries[1].Content, "line two")
	}
}

func TestOutputBuffer_NonOutputPassThrough(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, testLogger(t))
	defer w.Close()

	buf := NewOutputBuffer(w, "crew1", "agent1")

	if err := buf.Append(LogEntry{Event: "output", Content: "buffered "}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "tool_use", Content: "bash", Tool: "bash"}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "output", Content: "after tool"}); err != nil {
		t.Fatal(err)
	}
	buf.Close()

	entries, err := readJSONL(filepath.Join(dir, "crews", "crew1", "agents", "agent1", "current.jsonl"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Non-output event should flush the buffer first, then pass through
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Event != "output" || entries[0].Content != "buffered " {
		t.Errorf("entry[0] = %v", entries[0])
	}
	if entries[1].Event != "tool_use" {
		t.Errorf("entry[1] = %v", entries[1])
	}
	if entries[2].Event != "output" || entries[2].Content != "after tool" {
		t.Errorf("entry[2] = %v", entries[2])
	}
}

func TestOutputBuffer_TextAndThinkingEvents(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, testLogger(t))
	defer w.Close()

	buf := NewOutputBuffer(w, "crew1", "agent1")

	if err := buf.Append(LogEntry{Event: "thinking", Content: "Let me "}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "thinking", Content: "think..."}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "text", Content: "Hello "}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "text", Content: "world!"}); err != nil {
		t.Fatal(err)
	}
	buf.Close()

	entries, err := readJSONL(filepath.Join(dir, "crews", "crew1", "agents", "agent1", "current.jsonl"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (thinking + text), got %d", len(entries))
	}
	if entries[0].Event != "thinking" || entries[0].Content != "Let me think..." {
		t.Errorf("entry[0] = event=%q content=%q", entries[0].Event, entries[0].Content)
	}
	if entries[1].Event != "text" || entries[1].Content != "Hello world!" {
		t.Errorf("entry[1] = event=%q content=%q", entries[1].Event, entries[1].Content)
	}
}

func TestOutputBuffer_EventLevelMapping(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, testLogger(t))
	defer w.Close()

	buf := NewOutputBuffer(w, "crew1", "agent1")

	if err := buf.Append(LogEntry{Event: "error", Content: "something failed"}); err != nil {
		t.Fatal(err)
	}
	if err := buf.Append(LogEntry{Event: "system", Content: "sidecar restarted"}); err != nil {
		t.Fatal(err)
	}
	buf.Close()

	entries, err := readJSONL(filepath.Join(dir, "crews", "crew1", "agents", "agent1", "current.jsonl"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Level != "error" {
		t.Errorf("error event level = %q, want %q", entries[0].Level, "error")
	}
	if entries[1].Level != "warn" {
		t.Errorf("system event level = %q, want %q", entries[1].Level, "warn")
	}
}

func TestOutputBuffer_TimerFlush(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, testLogger(t))
	defer w.Close()

	buf := NewOutputBuffer(w, "crew1", "agent1")

	if err := buf.Append(LogEntry{Event: "output", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	// Poll for timer flush instead of fixed sleep
	path := filepath.Join(dir, "crews", "crew1", "agents", "agent1", "current.jsonl")
	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		var err error
		data, err = os.ReadFile(path)
		if err == nil && len(data) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(data) == 0 {
		t.Error("expected timer to flush buffered content, but file is empty after 2s")
	}

	buf.Close()
}
