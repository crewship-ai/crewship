package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// parseClaudeCodeStreamJSON stamps meta["decode_error"] on every event a
// partially-decoded line produced: the line kept whatever matched, and this
// records which field we could not read. Without a consumer that marker is the
// same shape of defect this whole round is about — written, never read — and
// the thing it reports is a field the CLI sent that we silently dropped.
//
// One WARN per run is the consumer. Not per event: a mistyped field usually
// repeats on every line of the stream, and a log that repeats stops being read.
func TestRunAgent_LogsAPartialDecodeOnce(t *testing.T) {
	t.Parallel()
	// `result` typed as an object: the envelope decodes, that field does not.
	const stream = `{"type":"system","subtype":"init","model":"claude-opus-4-8","result":{"text":"x"}}
{"type":"result","subtype":"success","is_error":false,"result":{"text":"done"}}
`
	var buf bytes.Buffer
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), captureSlog(&buf))
	o.SetJournal(&covJournal{})
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	lines := decodeLogLines(t, &buf)
	var warns []map[string]any
	for _, l := range lines {
		if msg, _ := l["msg"].(string); strings.Contains(msg, "stream line only partly decoded") {
			warns = append(warns, l)
		}
	}
	if len(warns) == 0 {
		t.Fatalf("a field the CLI sent was dropped and nothing said so; logged:\n%s", buf.String())
	}
	if len(warns) > 1 {
		t.Errorf("logged %d times for one run; a per-line log for a per-stream fault stops being read", len(warns))
	}
	if got, _ := warns[0]["level"].(string); got != "WARN" {
		t.Errorf("level = %q, want WARN — we lost data the CLI sent", got)
	}
	if detail, _ := warns[0]["decode_error"].(string); detail == "" {
		t.Error("the log line does not carry json's own error, which is the only thing naming the field")
	}
}

// A clean stream must stay quiet, or the signal is noise.
func TestRunAgent_NoPartialDecodeLogOnACleanStream(t *testing.T) {
	t.Parallel()
	const stream = `{"type":"system","subtype":"init","model":"claude-opus-4-8"}
{"type":"result","subtype":"success","is_error":false,"result":"done"}
`
	var buf bytes.Buffer
	o := New(covNewRunContainer(covRunOpts{stream: stream}), newMemState(), captureSlog(&buf))
	o.SetJournal(&covJournal{})
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if strings.Contains(buf.String(), "partly decoded") {
		t.Errorf("a clean stream logged a decode warning:\n%s", buf.String())
	}
}
