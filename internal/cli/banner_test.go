package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A bytes.Buffer is never an *os.File TTY, so both banners must no-op on it
// regardless of env — this guards machine-readable / CI output from stray
// escape codes.
func TestBanners_NonTTYWriteNothing(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")

	var logo bytes.Buffer
	PrintLogo(&logo, "v1.2.3")
	if logo.Len() != 0 {
		t.Fatalf("PrintLogo wrote %d bytes to a non-TTY writer, want 0", logo.Len())
	}

	var line bytes.Buffer
	PrintStartLine(&line, "v1.2.3")
	if line.Len() != 0 {
		t.Fatalf("PrintStartLine wrote %d bytes to a non-TTY writer, want 0", line.Len())
	}
}

// colorLevel gates on the terminal being a TTY (and on NO_COLOR / TERM=dumb)
// before it inspects COLORTERM. A non-TTY writer is always colorNone — the
// branch that keeps pipes clean — and is unit-testable without a real tty.
func TestColorLevel_NonTTYIsNone(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	var buf bytes.Buffer
	if got := colorLevel(&buf); got != colorNone {
		t.Fatalf("colorLevel(non-tty) = %v, want colorNone", got)
	}
}

// The embedded logo art must stay well-formed: a stable row count and a color
// reset at the end of every row, so a bad regeneration from the SVG is caught.
func TestLogoArt_WellFormed(t *testing.T) {
	if len(crewshipLogoArt) != 16 {
		t.Fatalf("expected 16 art rows, got %d", len(crewshipLogoArt))
	}
	for i, row := range crewshipLogoArt {
		if !strings.Contains(row, "\x1b[") {
			t.Errorf("row %d carries no ANSI escape", i)
		}
		if !strings.HasSuffix(row, "\x1b[0m") {
			t.Errorf("row %d does not reset color at end of line", i)
		}
	}
}

// The side text must fit beside the mark and carry a version slot PrintLogo can
// fill, so the centered wordmark stays valid as either side is edited.
func TestLogoSideText_Fits(t *testing.T) {
	if len(logoSideText) > len(crewshipLogoArt) {
		t.Fatalf("side text (%d) taller than the mark (%d)", len(logoSideText), len(crewshipLogoArt))
	}
	if !strings.Contains(strings.Join(logoSideText, "\n"), "%s") {
		t.Error("side text is missing its version placeholder")
	}
}
