package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestHighlightQuery_Basic(t *testing.T) {
	got := highlightQuery("the auth migration shipped", "auth")
	want := "the " + cli.Bold + "auth" + cli.Reset + " migration shipped"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestHighlightQuery_CaseInsensitive(t *testing.T) {
	got := highlightQuery("Auth Migration", "auth")
	if !strings.Contains(got, cli.Bold+"Auth"+cli.Reset) {
		t.Errorf("expected case-insensitive highlight, got %q", got)
	}
}

func TestHighlightQuery_MultipleHits(t *testing.T) {
	got := highlightQuery("auth and auth and auth", "auth")
	count := strings.Count(got, cli.Bold+"auth"+cli.Reset)
	if count != 3 {
		t.Errorf("expected 3 highlights, got %d in %q", count, got)
	}
}

func TestHighlightQuery_NoMatch(t *testing.T) {
	got := highlightQuery("nothing matching", "auth")
	if got != "nothing matching" {
		t.Errorf("expected pass-through, got %q", got)
	}
}

func TestHighlightQuery_EmptyInputs(t *testing.T) {
	if got := highlightQuery("", "auth"); got != "" {
		t.Errorf("empty s should return empty, got %q", got)
	}
	if got := highlightQuery("hi", ""); got != "hi" {
		t.Errorf("empty q should pass through, got %q", got)
	}
}

func TestBodySnippet_String(t *testing.T) {
	got := bodySnippet("the auth migration broke a query", "auth", 180)
	if !strings.Contains(stripANSI(got), "auth migration") {
		t.Errorf("missing content (stripped): %q", stripANSI(got))
	}
	if !strings.Contains(got, cli.Bold) {
		t.Errorf("expected highlight: %q", got)
	}
}

func TestBodySnippet_TruncatesAroundMatch(t *testing.T) {
	prefix := strings.Repeat("x ", 200)
	body := prefix + "the auth flag landed"
	got := bodySnippet(body, "auth", 80)
	if !strings.Contains(got, "auth") {
		t.Errorf("expected match in window: %q", got)
	}
	if !strings.HasPrefix(got, "...") {
		t.Errorf("expected leading ellipsis when match is far in: %q", got[:30])
	}
	// Strip ANSI to check raw length.
	raw := stripANSI(got)
	if len(raw) > 90 {
		t.Errorf("snippet too long: %d chars: %q", len(raw), raw)
	}
}

func TestBodySnippet_MapPicksTextField(t *testing.T) {
	got := bodySnippet(map[string]any{
		"text":    "the auth pattern is solid",
		"summary": "ignored",
	}, "auth", 180)
	if !strings.Contains(stripANSI(got), "auth pattern") {
		t.Errorf("expected text field used: %q", stripANSI(got))
	}
}

func TestBodySnippet_MapFallsThroughKeys(t *testing.T) {
	got := bodySnippet(map[string]any{
		"summary": "found in summary",
	}, "summary", 180)
	if !strings.Contains(stripANSI(got), "found in summary") {
		t.Errorf("expected summary fallback: %q", stripANSI(got))
	}
}

func TestBodySnippet_NewlinesCollapsed(t *testing.T) {
	got := bodySnippet("line one\nline two\nline three", "two", 180)
	if strings.Contains(got, "\n") {
		t.Errorf("expected newlines collapsed: %q", got)
	}
}

func TestBodySnippet_EmptyReturnsEmpty(t *testing.T) {
	if got := bodySnippet(nil, "x", 50); got != "" {
		t.Errorf("nil body should yield empty, got %q", got)
	}
	if got := bodySnippet(map[string]any{}, "x", 50); got != "" {
		t.Errorf("empty map should yield empty, got %q", got)
	}
}

func TestPluralize(t *testing.T) {
	if pluralize(1) != "" {
		t.Errorf("singular should be empty")
	}
	if pluralize(0) != "es" {
		t.Errorf("0 should pluralise")
	}
	if pluralize(5) != "es" {
		t.Errorf("5 should pluralise")
	}
}

// stripANSI removes ANSI escape sequences for length comparison in tests.
func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// skip until 'm'
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
