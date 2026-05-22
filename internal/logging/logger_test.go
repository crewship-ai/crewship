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

// TestRedactionPipesThroughLookout pins the audit M18 wiring: every
// string-valued attribute is scanned by lookout.Redact, so a stray
// bearer token, sk-..., or password=... in a log line is replaced by
// ***REDACTED:{kind}*** before it reaches stdout. Non-string attrs and
// built-in keys (time, level, msg) are not rewritten.
func TestRedactionPipesThroughLookout(t *testing.T) {
	var buf bytes.Buffer
	logger := New("info", "json", &buf)

	// A bearer token in an attribute value -- the canonical leak shape.
	logger.Info("upstream call failed",
		"endpoint", "https://api.example.com",
		"auth_header", "Bearer abc123def456ghi789jkl012mno345",
	)

	out := buf.String()
	// The redacted marker must be present and the raw token must NOT.
	if !strings.Contains(out, "***REDACTED") {
		t.Errorf("expected ***REDACTED marker in log output, got: %s", out)
	}
	if strings.Contains(out, "abc123def456ghi789jkl012mno345") {
		t.Errorf("raw bearer token leaked into log output: %s", out)
	}
	// Non-sensitive attributes pass through unchanged.
	if !strings.Contains(out, "https://api.example.com") {
		t.Errorf("benign endpoint attribute should pass through unchanged: %s", out)
	}
	// Built-in slog keys (msg, level, time) must remain in their canonical
	// shapes so downstream parsers don't break.
	if !strings.Contains(out, `"msg":"upstream call failed"`) {
		t.Errorf("msg key should not be rewritten: %s", out)
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
