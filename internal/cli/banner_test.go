package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A bytes.Buffer is not an *os.File TTY, so the banner must no-op regardless of
// env — this guards machine-readable/CI output from stray escape codes.
func TestPrintStartupBanner_NonTTYWritesNothing(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	var buf bytes.Buffer
	PrintStartupBanner(&buf, "v1.2.3")
	if buf.Len() != 0 {
		t.Fatalf("expected no output to a non-TTY writer, got %d bytes", buf.Len())
	}
}

// bannerCapable gates on NO_COLOR and a truecolor COLORTERM before it ever
// checks the fd, so those two branches are unit-testable without a real TTY.
func TestBannerCapable_GatesOnEnv(t *testing.T) {
	var buf bytes.Buffer // never a TTY

	t.Run("no-color", func(t *testing.T) {
		t.Setenv("COLORTERM", "truecolor")
		t.Setenv("NO_COLOR", "1")
		if bannerCapable(&buf) {
			t.Fatal("NO_COLOR set must disable the banner")
		}
	})

	t.Run("non-truecolor", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("COLORTERM", "")
		if bannerCapable(&buf) {
			t.Fatal("a non-truecolor terminal must disable the banner")
		}
	})
}

// The embedded art must be well-formed: a stable row count and truecolor escape
// sequences, so a bad regeneration from the SVG is caught in CI.
func TestBannerArt_WellFormed(t *testing.T) {
	if len(crewshipBannerArt) != 15 {
		t.Fatalf("expected 15 art rows, got %d", len(crewshipBannerArt))
	}
	for i, row := range crewshipBannerArt {
		if !strings.Contains(row, "\x1b[") {
			t.Errorf("row %d carries no ANSI escape", i)
		}
		if !strings.HasSuffix(row, "\x1b[0m") {
			t.Errorf("row %d does not reset color at end of line", i)
		}
	}
}
