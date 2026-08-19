package cli

// `--format yaml` and the opaque JSON document.
//
// A run carries `metadata`, an opaque JSON blob the server owns and the CLI
// passes through. Held as a raw byte slice it is JSON to encoding/json and a
// []byte to yaml.v3 — and yaml.v3 renders a []byte as a sequence of integers,
// one per line. `run list --format yaml` over a handful of runs printed
// hundreds of numbers where a mapping belonged, and cmd_preferences.go had
// already had to work around the same thing at its own call site.
//
// The fix is on the TYPE, not in the formatter: RawJSON implements
// yaml.Marshaler, so every encoder that meets one renders the document it
// holds. Fixing Formatter.YAML instead — by routing it through JSON — would
// have renamed the keys of every other command's YAML output, because the
// structs the formatter is handed disagree about which tag set they carry.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatterYAML_RawJSONRendersAsItsDocument(t *testing.T) {
	cases := []struct {
		name     string
		metadata string
		want     []string // substrings that must appear
		notWant  []string
	}{{
		name:     "an object renders as a mapping",
		metadata: `{"tokens_in":120,"model":"claude-opus-5"}`,
		want:     []string{"metadata:", "tokens_in: 120", "model: claude-opus-5"},
		// 123 is the byte value of "{" — the first line of the old output.
		notWant: []string{"- 123"},
	}, {
		name:     "a nested document keeps its shape",
		metadata: `{"usage":{"input":7,"output":9},"tags":["a","b"]}`,
		want:     []string{"input: 7", "output: 9", "- a", "- b"},
		notWant:  []string{"- 123"},
	}, {
		name: "a large integer stays an integer, not an exponent",
		// A round-trip through float64 would print 1.699999999e+09 for a
		// timestamp or a token count.
		metadata: `{"finished_unix":1699999999}`,
		want:     []string{"finished_unix: 1699999999"},
		notWant:  []string{"e+09"},
	}, {
		name:     "a JSON null renders as null",
		metadata: `null`,
		want:     []string{"metadata: null"},
		notWant:  []string{"- 110"},
	}, {
		name:     "an absent document renders as null rather than an empty sequence",
		metadata: ``,
		want:     []string{"metadata: null"},
		notWant:  []string{"metadata: []"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			f := &Formatter{Writer: &buf, Format: "yaml"}
			run := RunDetail{ID: "run_1", Status: "COMPLETED", Metadata: RawJSON(tc.metadata)}
			if err := f.YAML([]RunDetail{run}); err != nil {
				t.Fatalf("YAML: %v", err)
			}
			got := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("output does not contain %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.notWant {
				if strings.Contains(got, bad) {
					t.Errorf("output still contains %q — the document is being rendered "+
						"as its bytes:\n%s", bad, got)
				}
			}
		})
	}
}

// RawJSON is carried verbatim in the machine formats that already worked, and
// a decode/encode round trip must not reshape or re-indent the document.
func TestRawJSON_JSONRoundTripIsVerbatim(t *testing.T) {
	const doc = `{"b":1,"a":[2,3]}`

	var target struct {
		Metadata RawJSON `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(`{"metadata":`+doc+`}`), &target); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(target.Metadata) != doc {
		t.Errorf("decoded metadata = %q, want the bytes verbatim %q", target.Metadata, doc)
	}

	var buf bytes.Buffer
	f := &Formatter{Writer: &buf, Format: "json"}
	if err := f.JSON(target); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	// Key order and all: the CLI does not own this document.
	if !strings.Contains(buf.String(), `"b": 1`) {
		t.Errorf("the JSON output lost the document:\n%s", buf.String())
	}
}
