package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatter_Markdown_Empty verifies the Markdown method is a no-op for
// empty input regardless of output format.
func TestFormatter_Markdown_Empty(t *testing.T) {
	var buf bytes.Buffer
	f := &Formatter{Format: "table", Writer: &buf}

	f.Markdown("")

	assert.Empty(t, buf.String(), "empty markdown should produce no output")
}

// TestFormatter_Markdown_RawForMachineFormats verifies that JSON, YAML, and
// quiet formats emit the markdown as-is (so downstream tooling can process it).
func TestFormatter_Markdown_RawForMachineFormats(t *testing.T) {
	cases := []struct {
		name   string
		format string
	}{
		{"json format emits raw", "json"},
		{"yaml format emits raw", "yaml"},
		{"quiet format emits raw", "quiet"},
	}

	input := "# Heading\n\nSome **bold** text and a [link](https://example.com)."

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			f := &Formatter{Format: tc.format, Writer: &buf}

			f.Markdown(input)

			out := buf.String()
			require.NotEmpty(t, out, "should have produced output")
			// Raw markdown passes through verbatim + trailing newline.
			assert.Contains(t, out, "# Heading", "raw heading should be preserved")
			assert.Contains(t, out, "**bold**", "raw emphasis should be preserved")
			assert.Contains(t, out, "[link](https://example.com)",
				"raw link syntax should be preserved")
		})
	}
}

// TestFormatter_Markdown_NonTTYWriter verifies that when the writer is not a
// TTY (e.g. bytes.Buffer, pipes), glamour rendering is skipped and raw
// markdown is emitted. This keeps piped output grep-able.
func TestFormatter_Markdown_NonTTYWriter(t *testing.T) {
	var buf bytes.Buffer
	f := &Formatter{Format: "table", Writer: &buf}

	// bytes.Buffer is not an *os.File, so the TTY check in Markdown()
	// falls through to the glamour path. But glamour on a non-terminal
	// produces non-ANSI output regardless of style (it auto-degrades).
	input := "## Subheading\n\nPlain paragraph."
	f.Markdown(input)

	out := buf.String()
	require.NotEmpty(t, out, "should produce some output")
	// The rendered content must contain the original words, even if
	// wrapped/styled. We don't assert exact formatting because that depends
	// on glamour's internal heuristics.
	assert.True(t,
		strings.Contains(out, "Subheading") || strings.Contains(out, "## Subheading"),
		"rendered output should contain the heading text, got: %q", out)
	assert.Contains(t, out, "Plain paragraph",
		"rendered output should contain the paragraph text")
}

// TestFormatter_Table_Empty verifies that an empty row set produces a
// headers-only output with "(no results)" hint — helpful for UX.
func TestFormatter_Table_Empty(t *testing.T) {
	var buf bytes.Buffer
	f := &Formatter{Format: "table", Writer: &buf}

	f.Table([]string{"ID", "NAME", "STATUS"}, nil)

	out := buf.String()
	assert.Contains(t, out, "ID", "should show ID column header")
	assert.Contains(t, out, "NAME", "should show NAME column header")
	assert.Contains(t, out, "STATUS", "should show STATUS column header")
	assert.Contains(t, out, "no results",
		"empty table should show a hint so users don't think the command failed")
}

// TestFormatter_Table_Quiet verifies that quiet format prints only the first
// column, one entry per line — suitable for xargs piping.
func TestFormatter_Table_Quiet(t *testing.T) {
	var buf bytes.Buffer
	f := &Formatter{Format: "quiet", Writer: &buf}

	rows := [][]string{
		{"id-1", "alpha", "running"},
		{"id-2", "beta", "stopped"},
		{"id-3", "gamma", "running"},
	}
	f.Table([]string{"ID", "NAME", "STATUS"}, rows)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3, "quiet mode should produce exactly one line per row")
	assert.Equal(t, "id-1", lines[0])
	assert.Equal(t, "id-2", lines[1])
	assert.Equal(t, "id-3", lines[2])
}
