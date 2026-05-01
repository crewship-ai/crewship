package cli

import (
	"os"
	"strings"
	"testing"
)

// TestMain ensures color vars are at their default ANSI values before any
// markdown test runs, regardless of test ordering. Without this, a sibling
// test that calls InitColors(true) could leave color globals empty and make
// markdown styling assertions silently misleading.
func TestMain(m *testing.M) {
	resetDefaults()
	os.Exit(m.Run())
}

func TestMarkdown_Heading(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("# Title\n## Sub\n### Deep\n")
	if !strings.Contains(got, Bold+Cyan+"Title"+Reset) {
		t.Errorf("h1 missing: %q", got)
	}
	if !strings.Contains(got, Bold+Blue+"Sub"+Reset) {
		t.Errorf("h2 missing: %q", got)
	}
	if !strings.Contains(got, Bold+"Deep"+Reset) {
		t.Errorf("h3 missing: %q", got)
	}
}

func TestMarkdown_NotAHeading(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("#nospace\n")
	if strings.Contains(got, Bold) {
		t.Errorf("expected no styling for missing space: %q", got)
	}
}

func TestMarkdown_Bold(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("hello **world** there\n")
	if !strings.Contains(got, Bold+"world"+Reset) {
		t.Errorf("bold missing: %q", got)
	}
}

func TestMarkdown_InlineCode(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("use `go test` to run\n")
	if !strings.Contains(got, Gray+"`go test`"+Reset) {
		t.Errorf("inline code missing: %q", got)
	}
}

func TestMarkdown_FencedBlock(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("```go\nfunc main() {}\n```\nafter\n")
	if !strings.Contains(got, Gray+"func main() {}"+Reset) {
		t.Errorf("fenced content not styled: %q", got)
	}
	if !strings.Contains(got, "after") {
		t.Errorf("after-fence content missing: %q", got)
	}
	// `after` should NOT be inside a fenced block — i.e. Gray+"after" must not appear.
	if strings.Contains(got, Gray+"after") {
		t.Errorf("post-fence content wrongly styled as code: %q", got)
	}
}

func TestMarkdown_StreamingSafe(t *testing.T) {
	// Simulate the markdown arriving in arbitrary chunks. The output should be
	// identical to a one-shot Render.
	full := "# Title\n\nA **bold** line and a `code` span.\n\n```\nblock\n```\ntail\n"
	want := NewMarkdownRenderer().Render(full)

	r := NewMarkdownRenderer()
	var got strings.Builder
	for i := 0; i < len(full); i += 3 {
		end := i + 3
		if end > len(full) {
			end = len(full)
		}
		got.WriteString(r.Write(full[i:end]))
	}
	got.WriteString(r.Flush())

	if got.String() != want {
		t.Errorf("streaming differs from one-shot:\nstreamed: %q\none-shot: %q", got.String(), want)
	}
}

func TestMarkdown_StreamingDoesNotEmitPartialLine(t *testing.T) {
	r := NewMarkdownRenderer()
	out := r.Write("partial without newline")
	if out != "" {
		t.Errorf("expected no output before newline, got %q", out)
	}
	out2 := r.Write("\n")
	if !strings.Contains(out2, "partial without newline") {
		t.Errorf("expected emit on newline, got %q", out2)
	}
}

func TestMarkdown_BulletList(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("- item one\n- item two\n")
	if !strings.Contains(got, Yellow+"-"+Reset) {
		t.Errorf("bullet not styled: %q", got)
	}
	if !strings.Contains(got, "item one") {
		t.Errorf("content missing: %q", got)
	}
}

func TestMarkdown_OrderedList(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("1. first\n2. second\n")
	if !strings.Contains(got, Yellow+"1."+Reset) {
		t.Errorf("ordered marker missing: %q", got)
	}
	if !strings.Contains(got, Yellow+"2."+Reset) {
		t.Errorf("second marker missing: %q", got)
	}
}

func TestMarkdown_Link(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("see [docs](https://example.com) for more\n")
	if !strings.Contains(got, Cyan+"docs"+Reset) {
		t.Errorf("link text not styled: %q", got)
	}
	if !strings.Contains(got, "https://example.com") {
		t.Errorf("link href missing: %q", got)
	}
}

func TestMarkdown_BlockQuote(t *testing.T) {
	r := NewMarkdownRenderer()
	got := r.Render("> a quoted line\n")
	if !strings.Contains(got, "│") {
		t.Errorf("quote marker missing: %q", got)
	}
}

func TestMarkdown_Reset(t *testing.T) {
	r := NewMarkdownRenderer()
	r.Write("```\ncode")
	if !r.inFence {
		t.Fatal("expected inFence after open")
	}
	r.Reset()
	if r.inFence || r.pending.Len() != 0 {
		t.Errorf("Reset did not clear state")
	}
}

func TestMarkdown_InlineSpansSplitAcrossChunks(t *testing.T) {
	// The * delimiter is split across chunks. Because we line-buffer,
	// no partial styling should appear; the line is rendered when the
	// newline arrives.
	r := NewMarkdownRenderer()
	out1 := r.Write("hello **wo")
	out2 := r.Write("rld** there\n")
	combined := out1 + out2
	if !strings.Contains(combined, Bold+"world"+Reset) {
		t.Errorf("bold not assembled: %q", combined)
	}
}

func TestMarkdown_HeadingLevelHelper(t *testing.T) {
	cases := map[string]int{
		"# one":      1,
		"## two":     2,
		"###### six": 6,
		"####### no": 0, // 7 #'s — too many
		"#nospace":   0,
		"":           0,
		"#":          0, // # without space
	}
	for in, want := range cases {
		if got := headingLevel(in); got != want {
			t.Errorf("headingLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMarkdown_SplitKeepNewline(t *testing.T) {
	got := splitKeepNewline("a\nb\nc")
	if len(got) != 3 || got[0] != "a\n" || got[1] != "b\n" || got[2] != "c" {
		t.Errorf("unexpected split: %v", got)
	}
}
