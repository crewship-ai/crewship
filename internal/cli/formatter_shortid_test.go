package cli

import (
	"bytes"
	"strings"
	"testing"
)

// `--format quiet` renders rows[i][0] and nothing else — it exists so a script
// can pipe ids into the next command. Every list command that shortened its id
// for the table column shortened it for quiet too, and a 16-character prefix
// fed back into `run get` answers 404. ShortID is the one place that decision
// is made, so a new list command gets the quiet-safe behaviour by default
// instead of having to remember it.
func TestFormatterShortID(t *testing.T) {
	const full = "msg_1786552750441963636_4bd885e80dc9485e"

	cases := []struct {
		name   string
		format string
		short  string
		want   string
		why    string
	}{
		{"quiet/prefix", "quiet", full[:16], full, "quiet has no columns and its output is consumed by the next command"},
		{"quiet/ellipsis", "quiet", full[:15] + "…", full, "an ellipsis marker is still not an id"},
		{"table", "table", full[:16], full[:16], "the table has a column width to respect"},
		{"default", "", full[:16], full[:16], "the default renderer is the table"},
		{"json", "json", full[:16], full[:16], "json/yaml/ndjson never read the rows, so the cell is inert"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &Formatter{Format: tc.format}
			if got := f.ShortID(full, tc.short); got != tc.want {
				t.Errorf("ShortID(%q, %q) with format %q = %q, want %q — %s",
					full, tc.short, tc.format, got, tc.want, tc.why)
			}
		})
	}
}

// End to end through the renderer: the same rows must print the whole id under
// quiet and the shortened one in the table. This is the assertion every
// converted call site inherits.
func TestFormatterShortIDReachesTheQuietRenderer(t *testing.T) {
	const full = "msg_1786552750441963636_4bd885e80dc9485e"

	for _, format := range []string{"quiet", "table"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			f := &Formatter{Format: format, Writer: &buf}
			f.Table([]string{"ID", "NAME"}, [][]string{{f.ShortID(full, full[:16]), "alice"}})

			out := buf.String()
			if format == "quiet" {
				if out != full+"\n" {
					t.Errorf("quiet output = %q, want the whole id and nothing else", out)
				}
				return
			}
			if strings.Contains(out, full) {
				t.Errorf("table printed the untruncated id:\n%s", out)
			}
			if !strings.Contains(out, full[:16]) {
				t.Errorf("table lost the id prefix entirely:\n%s", out)
			}
		})
	}
}
