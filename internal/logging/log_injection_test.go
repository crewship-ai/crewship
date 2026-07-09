package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestLogInjection_AttrCannotForgeLine is the core log-injection (CWE-117)
// guarantee: an attacker-controlled string attribute carrying a CRLF +
// a fake log line must land on a SINGLE physical output line, with the
// newline escaped, so it cannot masquerade as a second record.
func TestLogInjection_AttrCannotForgeLine(t *testing.T) {
	const evil = "ok\nFAKE-LEVEL fabricated second line"
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			logger := New("info", format, &buf)
			logger.Info("real message", "user_input", evil)

			out := buf.String()
			// Exactly one trailing newline: the record terminator, nothing else.
			body := strings.TrimRight(out, "\n")
			if strings.Contains(body, "\n") {
				t.Fatalf("%s: forged newline survived into output, produced multiple lines:\n%q", format, out)
			}
			if !strings.Contains(out, `\n`) {
				t.Errorf("%s: expected the newline to be escaped to \\n, got: %q", format, out)
			}
			if !strings.Contains(out, "FAKE-LEVEL fabricated second line") {
				t.Errorf("%s: payload text should be preserved (escaped), got: %q", format, out)
			}
		})
	}
}

// TestLogInjection_MsgCannotForgeLine covers the message position, which
// is exempt from secret redaction but must still be control-neutralized:
// a fmt.Sprintf that interpolates tainted input into the msg is a common
// pattern the central handler has to defend too.
func TestLogInjection_MsgCannotForgeLine(t *testing.T) {
	var buf bytes.Buffer
	logger := New("info", "json", &buf)
	logger.Info("user said: hi\nERROR forged")

	out := buf.String()
	body := strings.TrimRight(out, "\n")
	if strings.Contains(body, "\n") {
		t.Fatalf("forged newline in msg survived, produced multiple lines:\n%q", out)
	}
	// JSON must still parse as a single object.
	var entry map[string]any
	if err := json.Unmarshal([]byte(body), &entry); err != nil {
		t.Fatalf("output is not a single JSON object: %v\n%q", err, out)
	}
	if msg, _ := entry["msg"].(string); !strings.Contains(msg, `\n`) {
		t.Errorf("expected escaped newline in msg field, got %q", msg)
	}
}

// TestLogInjection_CarriageReturnAndControls verifies CR and other C0
// controls never reach the wire as raw bytes. (The text handler may
// additionally re-quote the escaped form for display; what matters for
// injection is that no raw CR/ESC survives.)
func TestLogInjection_CarriageReturnAndControls(t *testing.T) {
	var buf bytes.Buffer
	logger := New("info", "text", &buf)
	logger.Info("m", "v", "a\rb\x1bc\td")

	out := buf.String()
	if strings.ContainsAny(strings.TrimRight(out, "\n"), "\r\x1b") {
		t.Errorf("CR / ESC leaked unescaped into output: %q", out)
	}
}

// TestNeutralizeControl_EscapesAndPreserves is the unit-level contract:
// CR/LF/ESC escaped, tab preserved as a raw byte (the enclosing handler
// decides how to render it downstream).
func TestNeutralizeControl_EscapesAndPreserves(t *testing.T) {
	got := neutralizeControl("a\rb\nc\x1bd\te")
	want := `a\rb\nc\x1bd` + "\t" + "e"
	if got != want {
		t.Errorf("neutralizeControl = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\r\n\x1b") {
		t.Errorf("raw control byte survived: %q", got)
	}
}

// TestNeutralizeControl_CleanStringUnchanged asserts the zero-allocation
// fast path: a value with no forgeable control byte is returned as-is.
func TestNeutralizeControl_CleanStringUnchanged(t *testing.T) {
	in := "a normal value with spaces, punctuation! and\tone tab"
	if got := neutralizeControl(in); got != in {
		t.Errorf("clean string mutated: got %q want %q", got, in)
	}
}

// TestLogInjection_ErrorAttrNeutralized pins the KindAny/error path: an
// error whose text carries a newline (the most common tainted non-string
// attr — dial/parse errors embed remote-controlled text) must not forge a
// second log line.
func TestLogInjection_ErrorAttrNeutralized(t *testing.T) {
	var buf bytes.Buffer
	logger := New("debug", "json", &buf)

	logger.Info("op failed", "error", errors.New("dial failed\nFAKE-LEVEL forged line"))

	out := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("error attr forged a second line: %q", out)
	}
	if !strings.Contains(out, `dial failed\\n`) && !strings.Contains(out, `dial failed\n`) {
		t.Errorf("expected escaped newline in error attr, got: %q", out)
	}
}

// TestRingHandler_CapturesNeutralizedAndRedacted pins the ring buffer's
// capture path: the ring stores values BEFORE the inner handler's
// ReplaceAttr hook runs, so it must apply the same redact+neutralize
// barrier itself — no raw newlines and no unredacted secrets at rest.
func TestRingHandler_CapturesNeutralizedAndRedacted(t *testing.T) {
	buffer := NewRingBuffer(8)
	inner := slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewRingHandler(inner, buffer))

	logger.Info("m\nforged-msg",
		"user_input", "evil\nFAKE-LINE",
		"auth", "Bearer sk-abc123def456ghi789jkl012mno345pqr")

	recs := buffer.Entries(8)
	if len(recs) != 1 {
		t.Fatalf("expected 1 captured record, got %d", len(recs))
	}
	rec := recs[0]
	if strings.Contains(rec.Message, "\n") {
		t.Errorf("ring stored raw newline in message: %q", rec.Message)
	}
	if strings.Contains(rec.Attrs["user_input"], "\n") {
		t.Errorf("ring stored raw newline in attr: %q", rec.Attrs["user_input"])
	}
	if strings.Contains(rec.Attrs["auth"], "sk-abc123def456ghi789jkl012mno345pqr") {
		t.Errorf("ring stored unredacted secret: %q", rec.Attrs["auth"])
	}
}
