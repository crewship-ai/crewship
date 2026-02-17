package fileserver

import (
	"context"
	"log/slog"
	"testing"
)

func TestWatchInvalidCrewID(t *testing.T) {
	w := NewWatcher(t.TempDir(), slog.Default(), nil)

	tests := []struct {
		name   string
		crewID string
	}{
		{"empty", ""},
		{"absolute path", "/etc/passwd"},
		{"parent traversal", "../escape"},
		{"nested traversal", "team/../escape"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := w.Watch(context.Background(), tt.crewID)
			if err == nil {
				t.Error("expected error for invalid crew ID")
			}
		})
	}
}

func TestExtractAgentSlug(t *testing.T) {
	tests := []struct {
		name     string
		relPath  string
		expected string
	}{
		{"simple", "claude-dev/file.txt", "claude-dev"},
		{"nested", "claude-dev/sub/file.txt", "claude-dev"},
		{"root file", "file.txt", "file.txt"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractAgentSlug(tt.relPath)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestWatchValidTeamCreatesDir(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(dir, slog.Default(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := w.Watch(ctx, "valid-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
