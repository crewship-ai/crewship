package logcollector

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriterAppend(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, slog.Default())
	defer w.Close()

	entry := LogEntry{
		Timestamp: time.Date(2026, 2, 16, 10, 0, 0, 0, time.UTC),
		Level:     "info",
		Agent:     "anna",
		Event:     "text",
		Content:   "Hello world",
	}

	if err := w.Append("crew-1", "agent-1", entry); err != nil {
		t.Fatal(err)
	}
	w.Flush()

	path := filepath.Join(dir, "crews", "crew-1", "agents", "agent-1", "current.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) == 0 {
		t.Fatal("expected non-empty log file")
	}
}

func TestWriterMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, slog.Default())
	defer w.Close()

	for i := 0; i < 5; i++ {
		if err := w.Append("crew-1", "agent-1", LogEntry{
			Event:   "text",
			Content: "line",
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()

	reader := NewReader(dir)
	entries, err := reader.ReadAgentLogs("crew-1", "agent-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}
}

func TestReaderWithOffset(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, slog.Default())
	defer w.Close()

	for i := 0; i < 10; i++ {
		if err := w.Append("crew-1", "agent-1", LogEntry{
			Event:   "text",
			Content: "line",
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()

	reader := NewReader(dir)
	entries, err := reader.ReadAgentLogs("crew-1", "agent-1", 5, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestReaderMissingFile(t *testing.T) {
	reader := NewReader(t.TempDir())
	entries, err := reader.ReadAgentLogs("crew-x", "agent-x", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Fatalf("expected nil, got %v", entries)
	}
}

func TestWriterDefaultTimestamp(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, slog.Default())
	defer w.Close()

	if err := w.Append("crew-1", "agent-1", LogEntry{
		Event:   "text",
		Content: "no timestamp",
	}); err != nil {
		t.Fatal(err)
	}
	w.Flush()

	reader := NewReader(dir)
	entries, err := reader.ReadAgentLogs("crew-1", "agent-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("expected 1 entry")
	}
	if entries[0].Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
}
