package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewJSONLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := New("info", "json", &buf)

	logger.Info("test message", "key", "value")

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("expected valid JSON, got: %s", buf.String())
	}
	if entry["msg"] != "test message" {
		t.Errorf("expected msg 'test message', got %v", entry["msg"])
	}
	if entry["key"] != "value" {
		t.Errorf("expected key 'value', got %v", entry["key"])
	}
}

func TestNewTextLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := New("debug", "text", &buf)

	logger.Debug("debug msg")

	if !strings.Contains(buf.String(), "debug msg") {
		t.Errorf("expected 'debug msg' in output: %s", buf.String())
	}
}

func TestLogLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New("warn", "json", &buf)

	logger.Info("should not appear")
	if buf.Len() > 0 {
		t.Error("info should be filtered at warn level")
	}

	logger.Warn("should appear")
	if buf.Len() == 0 {
		t.Error("warn should appear at warn level")
	}
}

func TestContextRoundTrip(t *testing.T) {
	logger := New("info", "json", nil)
	ctx := WithContext(context.Background(), logger)
	got := FromContext(ctx)

	if got != logger {
		t.Error("expected same logger from context")
	}
}

func TestFromContextDefault(t *testing.T) {
	got := FromContext(context.Background())
	if got == nil {
		t.Error("expected default logger, got nil")
	}
	if got != slog.Default() {
		t.Error("expected slog.Default()")
	}
}

// TestParseLevelAccepts verifies common case/spelling variants resolve to
// the level the operator obviously meant. Without this, CREWSHIP_LOG_LEVEL
// values like "WARN", "warning", or "fatal" silently downgrade to info,
// producing a much noisier log than expected.
func TestParseLevelAccepts(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"Debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"fatal", slog.LevelError},
	}
	for _, tc := range cases {
		got := parseLevel(tc.in)
		if got != tc.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
