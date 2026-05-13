package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNDJSON_Slice(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	rows := []interface{}{
		map[string]any{"id": "1", "name": "a"},
		map[string]any{"id": "2", "name": "b"},
	}
	if err := f.NDJSON(rows); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Errorf("want 2 lines, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "{") {
			t.Errorf("line should be JSON object: %q", l)
		}
	}
}

func TestNDJSON_SingleObject(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	if err := f.NDJSON(map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), `{"k":"v"}`) {
		t.Errorf("got=%q", b.String())
	}
}

func TestWriteNDJSONRow(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Writer: &b}
	for _, v := range []map[string]any{{"a": 1}, {"a": 2}, {"a": 3}} {
		if err := f.WriteNDJSONRow(v); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 {
		t.Errorf("got %d lines, want 3: %q", len(lines), b.String())
	}
}

func TestNDJSON_TypedSlice(t *testing.T) {
	// Regression test: passing a `[]T` (concrete) instead of `[]any`
	// previously encoded the whole slice as a single JSON array on one
	// line, which broke `--format ndjson` for every command that uses
	// typed result slices.
	type row struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	in := []row{{"1", "a"}, {"2", "b"}, {"3", "c"}}
	if err := f.NDJSON(in); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 {
		t.Errorf("want 3 lines for typed slice, got %d: %q", len(lines), b.String())
	}
}

func TestAuto_NDJSONRouting(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	if err := f.Auto([]interface{}{"x", "y"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 {
		t.Errorf("want 2 lines, got %d: %q", len(lines), b.String())
	}
}
