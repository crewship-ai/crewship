package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A bytes.Buffer is never an *os.File TTY, so PrintStartLine must no-op on it
// regardless of env — this guards machine-readable / CI output from stray
// escape codes.
func TestPrintStartLine_NonTTYWritesNothing(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	var buf bytes.Buffer
	PrintStartLine(&buf, "v1.2.3")
	if buf.Len() != 0 {
		t.Fatalf("PrintStartLine wrote %d bytes to a non-TTY writer, want 0", buf.Len())
	}
}

// startLine must render the name and version at every color level and must
// NEVER emit a Go format error like "%!(EXTRA …)" — the bug that leaked into
// the first cut when a no-verb line was passed through fmt.Sprintf with an
// argument.
func TestStartLine_NoFormatLeak(t *testing.T) {
	for _, lvl := range []colorCap{colorNone, colorBasic, colorTruecolor} {
		out := startLine("nightly-20260712-r463", lvl)
		if strings.Contains(out, "%!") {
			t.Errorf("level %v leaked a format directive: %q", lvl, out)
		}
		if !strings.Contains(out, "Crewship") {
			t.Errorf("level %v missing name: %q", lvl, out)
		}
		if !strings.Contains(out, "nightly-20260712-r463") {
			t.Errorf("level %v missing version: %q", lvl, out)
		}
		if !strings.Contains(out, "⛵") {
			t.Errorf("level %v missing sail glyph: %q", lvl, out)
		}
	}
}

// An empty version falls back to "dev" rather than rendering a dangling space.
func TestStartLine_EmptyVersionFallsBackToDev(t *testing.T) {
	if !strings.Contains(startLine("", colorNone), "dev") {
		t.Error("empty version should fall back to dev")
	}
}

// colorLevel gates on the writer being a TTY before it inspects COLORTERM, so a
// non-TTY writer is always colorNone — the branch that keeps pipes clean.
func TestColorLevel_NonTTYIsNone(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	var buf bytes.Buffer
	if got := colorLevel(&buf); got != colorNone {
		t.Fatalf("colorLevel(non-tty) = %v, want colorNone", got)
	}
}
