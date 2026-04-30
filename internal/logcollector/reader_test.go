package logcollector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestLogFile(t *testing.T, dir string, entries []LogEntry) string {
	t.Helper()
	path := filepath.Join(dir, "crews", "crew-1", "agents", "agent-1", "current.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range entries {
		data, _ := json.Marshal(e)
		f.Write(data)
		f.Write([]byte("\n"))
	}
	return dir
}

func TestReadAgentLogsHappyPath(t *testing.T) {
	dir := t.TempDir()
	entries := []LogEntry{
		{Timestamp: time.Now(), Level: "info", Agent: "agent-1", Event: "output", Content: "line 1"},
		{Timestamp: time.Now(), Level: "info", Agent: "agent-1", Event: "output", Content: "line 2"},
		{Timestamp: time.Now(), Level: "info", Agent: "agent-1", Event: "output", Content: "line 3"},
	}
	writeTestLogFile(t, dir, entries)

	reader := NewReader(dir)
	result, err := reader.ReadAgentLogs("crew-1", "agent-1", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result))
	}
	if result[0].Content != "line 1" {
		t.Errorf("expected 'line 1', got %q", result[0].Content)
	}
}

func TestReadAgentLogsWithOffset(t *testing.T) {
	dir := t.TempDir()
	entries := []LogEntry{
		{Timestamp: time.Now(), Level: "info", Content: "line 1"},
		{Timestamp: time.Now(), Level: "info", Content: "line 2"},
		{Timestamp: time.Now(), Level: "info", Content: "line 3"},
	}
	writeTestLogFile(t, dir, entries)

	reader := NewReader(dir)
	result, err := reader.ReadAgentLogs("crew-1", "agent-1", 1, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (offset=1), got %d", len(result))
	}
}

func TestReadAgentLogsWithLimit(t *testing.T) {
	dir := t.TempDir()
	entries := []LogEntry{
		{Timestamp: time.Now(), Level: "info", Content: "line 1"},
		{Timestamp: time.Now(), Level: "info", Content: "line 2"},
		{Timestamp: time.Now(), Level: "info", Content: "line 3"},
	}
	writeTestLogFile(t, dir, entries)

	reader := NewReader(dir)
	result, err := reader.ReadAgentLogs("crew-1", "agent-1", 0, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (limit=2), got %d", len(result))
	}
}

func TestReadAgentLogsMissingFile(t *testing.T) {
	dir := t.TempDir()
	reader := NewReader(dir)
	result, err := reader.ReadAgentLogs("crew-x", "agent-x", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for missing file, got %v", result)
	}
}

func TestValidatePathSegment(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"valid uuid", "550e8400-e29b-41d4-a716-446655440000", true},
		{"valid slug", "crew-alpha", true},
		{"empty", "", false},
		{"with slash", "team/bad", false},
		{"with backslash", "team\\bad", false},
		{"with dotdot", "team..bad", false},
		{"just dotdot", "..", false},
		// New defenses against control / whitespace / oversize input.
		// Null byte truncates paths in some FS layers; control chars and
		// whitespace produce surprising filenames; 1KB segments fail
		// every reasonable identifier shape.
		{"with null byte", "ok\x00bad", false},
		{"with newline", "ok\nbad", false},
		{"with space", "ok bad", false},
		{"with del char", "ok\x7fbad", false},
		{"oversize", strings.Repeat("a", 1024), false},
		// 0xFF 0xFE is not a valid UTF-8 sequence; without an explicit
		// utf8.ValidString gate, the rune iteration would decode it to
		// U+FFFD (the replacement char) which is both printable and
		// non-space and would leak through.
		{"invalid utf-8", "ok\xff\xfebad", false},
		// Boundary: 256 bytes of ASCII is the documented max length and
		// must be accepted; one more must not.
		{"max length 256", strings.Repeat("a", 256), true},
		{"max length plus one", strings.Repeat("a", 257), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePathSegment(tt.input)
			if tt.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected error for invalid input")
			}
		})
	}
}

func TestReadAgentLogsInvalidCrewID(t *testing.T) {
	reader := NewReader(t.TempDir())
	_, err := reader.ReadAgentLogs("../escape", "agent-1", 0, 0)
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestReadAgentLogsInvalidAgentID(t *testing.T) {
	reader := NewReader(t.TempDir())
	_, err := reader.ReadAgentLogs("crew-1", "", 0, 0)
	if err == nil {
		t.Error("expected error for empty agent ID")
	}
}
