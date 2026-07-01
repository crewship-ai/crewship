package orchestrator

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

func TestModelFamily(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":              "opus",
		"claude-sonnet-4-5-20250101":   "sonnet",
		"claude-haiku-4-5-20251001":    "haiku",
		"CLAUDE-OPUS-4-8":              "opus",
		"gpt-4o":                       "",
		"":                             "",
		"claude-3-5-sonnet-20240620":   "sonnet",
		"us.anthropic.claude-opus-4-8": "opus",
	}
	for in, want := range cases {
		if got := modelFamily(in); got != want {
			t.Errorf("modelFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

// captureSlog returns a logger that writes JSON records to buf so tests can
// assert on the emitted level/msg/fields.
func captureSlog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestLogResolvedModel_WarnOnFamilyFallback(t *testing.T) {
	var buf bytes.Buffer
	logResolvedModel(captureSlog(&buf), "agent_1", "claude-opus-4-8", "claude-sonnet-4-5")

	lines := decodeLogLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("expected Info + Warn (2 lines), got %d: %s", len(lines), buf.String())
	}
	// Info line carries the resolution facts.
	if lines[0]["level"] != "INFO" || lines[0]["msg"] != "agent model resolved" {
		t.Errorf("first line not the info resolution: %+v", lines[0])
	}
	if lines[0]["requested_model"] != "claude-opus-4-8" || lines[0]["actual_model"] != "claude-sonnet-4-5" {
		t.Errorf("info fields wrong: %+v", lines[0])
	}
	// Second line is the loud fallback warning.
	if lines[1]["level"] != "WARN" {
		t.Errorf("expected WARN on opus→sonnet fallback, got %+v", lines[1])
	}
}

func TestLogResolvedModel_NoWarnWhenFamilyMatches(t *testing.T) {
	var buf bytes.Buffer
	// Requested opus, actually served opus (different point release) — no warn.
	logResolvedModel(captureSlog(&buf), "agent_1", "claude-opus-4-8", "claude-opus-4-8-20260601")

	for _, l := range decodeLogLines(t, &buf) {
		if l["level"] == "WARN" {
			t.Errorf("unexpected WARN when families match: %+v", l)
		}
	}
}

func TestLogResolvedModel_BlankActualIsNoop(t *testing.T) {
	var buf bytes.Buffer
	logResolvedModel(captureSlog(&buf), "agent_1", "claude-opus-4-8", "")
	if buf.Len() != 0 {
		t.Errorf("blank actual model should emit nothing, got %s", buf.String())
	}
}

func TestLogResolvedModel_NoWarnWhenRequestedUnset(t *testing.T) {
	var buf bytes.Buffer
	// No requested override (subscription default) — we log the actual but
	// cannot claim a fallback, so no WARN.
	logResolvedModel(captureSlog(&buf), "agent_1", "", "claude-sonnet-4-5")
	for _, l := range decodeLogLines(t, &buf) {
		if l["level"] == "WARN" {
			t.Errorf("unexpected WARN when requested model is empty: %+v", l)
		}
	}
}

func TestBufferingHandler_CapturesResolvedModel(t *testing.T) {
	handler, acc := NewBufferingHandler(BufferingHandlerOpts{
		AgentSlug:         "agent1",
		CaptureResultMeta: true,
	})
	ts := time.Now().UTC()
	// The claude adapter emits the resolved model on the system/init event.
	handler(AgentEvent{
		Type:      "system",
		Metadata:  map[string]interface{}{"subtype": "init", "model": "claude-sonnet-4-5"},
		Timestamp: ts,
	})
	handler(AgentEvent{Type: "text", Content: "hi", Timestamp: ts})

	if got := acc.ResolvedModel(); got != "claude-sonnet-4-5" {
		t.Errorf("acc.ResolvedModel() = %q, want claude-sonnet-4-5", got)
	}
}

func TestBufferingHandler_ResolvedModelDisabled(t *testing.T) {
	handler, acc := NewBufferingHandler(BufferingHandlerOpts{AgentSlug: "agent1"})
	handler(AgentEvent{
		Type:      "system",
		Metadata:  map[string]interface{}{"subtype": "init", "model": "claude-sonnet-4-5"},
		Timestamp: time.Now(),
	})
	if acc.ResolvedModel() != "" {
		t.Errorf("ResolvedModel should be empty when capture disabled, got %q", acc.ResolvedModel())
	}
}
