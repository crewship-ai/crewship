package cli

import (
	"errors"
	"strings"
	"testing"
)

// errWriter fails every write with a fixed sentinel, standing in for the
// failure these renderers actually meet in the field: a closed pipe when the
// operator hangs a `| head -1` off a `--format json` run.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// The renderers must name themselves on failure and must not lose the cause.
//
// What reaches these returns is almost always a write error, and its own text
// ("write |1: broken pipe") says nothing about what was being written. The
// context is added once at the leaf rather than at AutoHuman/Auto/Machine/
// AutoDetail and the ~110 direct call sites, so the message reads the same
// whichever door the render came through.
func TestRenderersWrapWriteErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("pipe is closed")

	tests := []struct {
		name       string
		format     string
		value      interface{}
		render     func(f *Formatter, v interface{}) error
		wantPrefix string
		// noUnwrap marks a renderer whose library flattens the cause before we
		// ever see it, so errors.Is cannot reach the sentinel however we wrap.
		noUnwrap bool
	}{
		{
			name:       "JSON",
			value:      map[string]string{"k": "v"},
			render:     func(f *Formatter, v interface{}) error { return f.JSON(v) },
			wantPrefix: "render JSON: ",
		},
		{
			// gopkg.in/yaml.v3 turns a write failure into its own
			// `yaml: write error: …` value with no Unwrap method, so the chain
			// is already broken below us: errors.Is on a YAML render error can
			// never match the underlying cause. The text survives, which is
			// what the message-level assertion below checks. Pinned here so
			// nobody writes an errors.Is check against a YAML render error and
			// wonders why it is always false.
			name:       "YAML",
			value:      map[string]string{"k": "v"},
			render:     func(f *Formatter, v interface{}) error { return f.YAML(v) },
			wantPrefix: "render YAML: ",
			noUnwrap:   true,
		},
		{
			name:       "NDJSON scalar",
			value:      map[string]string{"k": "v"},
			render:     func(f *Formatter, v interface{}) error { return f.NDJSON(v) },
			wantPrefix: "render NDJSON: ",
		},
		{
			// The per-row loop is a separate return and used to hand back a
			// bare encoder error; the row index is what tells you how much of
			// the stream a consumer already got.
			name:       "NDJSON slice names the row",
			value:      []map[string]string{{"k": "v"}, {"k": "w"}},
			render:     func(f *Formatter, v interface{}) error { return f.NDJSON(v) },
			wantPrefix: "render NDJSON row 0: ",
		},
		{
			name:       "WriteNDJSONRow",
			value:      map[string]string{"k": "v"},
			render:     func(f *Formatter, v interface{}) error { return f.WriteNDJSONRow(v) },
			wantPrefix: "write NDJSON row: ",
		},
		{
			// AutoHuman's machine branch inherits the context from the leaf
			// rather than restating it — the point of fixing it at the leaf.
			name:   "AutoHuman routed to json",
			format: "json",
			value:  map[string]string{"k": "v"},
			render: func(f *Formatter, v interface{}) error {
				return f.AutoHuman(v, func() { t.Error("human ran for --format json") })
			},
			wantPrefix: "render JSON: ",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &Formatter{Format: tc.format, Writer: errWriter{err: sentinel}}
			err := tc.render(f, tc.value)
			if err == nil {
				t.Fatal("a failing writer produced no error")
			}
			if !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Errorf("error = %q, want it to start with %q", err, tc.wantPrefix)
			}
			if !strings.Contains(err.Error(), sentinel.Error()) {
				t.Errorf("wrapping dropped the cause from the message: %q", err)
			}
			if got := errors.Is(err, sentinel); got == tc.noUnwrap {
				if tc.noUnwrap {
					t.Errorf("errors.Is now reaches the cause through %s — "+
						"drop noUnwrap, the library started unwrapping", tc.name)
				} else {
					t.Errorf("wrapping broke the error chain: %v", err)
				}
			}
		})
	}
}
