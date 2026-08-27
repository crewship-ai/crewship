package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A nil slice must reach the wire as `[]`, never `null` (#2086).
//
// Go marshals `var rows []row` as `null` and `[]row{}` as `[]`, and which one
// a list command produces depends on whether its accumulator loop ran at all.
// So the same command answers `[]` on a populated workspace and `null` on an
// empty one — meaning `crewship prompt list -f json | jq '.[]'` works while
// you are testing it and fails with "Cannot iterate over null" on a fresh
// install. That is the worst possible distribution of a bug: absent exactly
// where it is developed, present exactly where it is first used.
//
// `prompt list`, `slash list` and `paymaster subscriptions` all shipped it.
// The fix is in Formatter, not in those three commands, because a call-site
// fix is one the next list command reintroduces by default.
//
// internal/api settled the same question for the server in
// TestListUsers_EmptyIsArray. These are the CLI half.

type nilSliceRow struct {
	Name string `json:"name"`
}

func captureFormatter(format string) (*Formatter, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &Formatter{Format: format, Writer: buf}, buf
}

func TestJSONNilSliceEncodesAsArray(t *testing.T) {
	t.Parallel()

	var typed []nilSliceRow // nil
	var untyped []interface{}
	var strs []string

	for _, tc := range []struct {
		name string
		in   interface{}
	}{
		{"typed struct slice", typed},
		{"untyped slice", untyped},
		{"string slice", strs},
	} {
		f, buf := captureFormatter("json")
		if err := f.JSON(tc.in); err != nil {
			t.Fatalf("%s: JSON: %v", tc.name, err)
		}
		got := strings.TrimSpace(buf.String())
		if got != "[]" {
			t.Errorf("%s: JSON(nil slice) = %q, want %q — `jq '.[]'` cannot iterate null",
				tc.name, got, "[]")
		}
		// The real contract is not the bytes, it is that a consumer can
		// iterate the result. Prove that directly.
		var out []interface{}
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Errorf("%s: output does not decode into a slice: %v", tc.name, err)
		}
	}
}

func TestYAMLNilSliceEncodesAsEmptySequence(t *testing.T) {
	t.Parallel()

	var rows []nilSliceRow
	f, buf := captureFormatter("yaml")
	if err := f.YAML(rows); err != nil {
		t.Fatalf("YAML: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != "[]" {
		t.Errorf("YAML(nil slice) = %q, want %q — `null` is not a sequence", got, "[]")
	}
}

// Machine() defaults to JSON for table/quiet/empty, so it must inherit the
// same guarantee — `onboarding status` and friends go through it.
func TestMachineNilSliceEncodesAsArray(t *testing.T) {
	t.Parallel()

	var rows []nilSliceRow
	for _, format := range []string{"", "table", "quiet", "json"} {
		f, buf := captureFormatter(format)
		if err := f.Machine(rows); err != nil {
			t.Fatalf("format %q: Machine: %v", format, err)
		}
		if got := strings.TrimSpace(buf.String()); got != "[]" {
			t.Errorf("format %q: Machine(nil slice) = %q, want %q", format, got, "[]")
		}
	}
}

// Auto/AutoDetail/AutoHuman all delegate to JSON/YAML/NDJSON, so the
// guarantee has to hold through each of them or a command picks up `null`
// again by choosing a different helper.
func TestAutoHelpersNilSliceEncodesAsArray(t *testing.T) {
	t.Parallel()

	var rows []nilSliceRow

	f, buf := captureFormatter("json")
	if err := f.Auto(rows, []string{"NAME"}, nil); err != nil {
		t.Fatalf("Auto: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("Auto(nil slice) = %q, want %q", got, "[]")
	}

	f, buf = captureFormatter("json")
	if err := f.AutoDetail(rows, nil); err != nil {
		t.Fatalf("AutoDetail: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("AutoDetail(nil slice) = %q, want %q", got, "[]")
	}

	f, buf = captureFormatter("json")
	if err := f.AutoHuman(rows, func() { t.Error("human renderer ran under -f json") }); err != nil {
		t.Fatalf("AutoHuman: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("AutoHuman(nil slice) = %q, want %q", got, "[]")
	}
}

// A nil slice nested inside a struct field is NOT rewritten — reflection over
// every field of every payload would be a different and much more invasive
// change, and no consumer iterates the top level of a struct. This test pins
// the boundary so the limitation is a decision on the record rather than a
// surprise found in an incident.
func TestNilSliceInsideStructIsNotRewritten(t *testing.T) {
	t.Parallel()

	type envelope struct {
		Items []nilSliceRow `json:"items"`
	}
	f, buf := captureFormatter("json")
	if err := f.JSON(envelope{}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"items": null`) {
		t.Errorf("expected nested nil slice to stay null (documented limit); got:\n%s", buf.String())
	}
}

// A nil map stays `null`. `{}` and `null` mean different things for an
// object — "no properties" versus "absent" — and nothing iterates a map the
// way `.[]` iterates an array, so rewriting it would change meaning to buy
// nothing.
func TestNilMapStaysNull(t *testing.T) {
	t.Parallel()

	var m map[string]string
	f, buf := captureFormatter("json")
	if err := f.JSON(m); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "null" {
		t.Errorf("JSON(nil map) = %q, want %q", got, "null")
	}
}

// A non-empty slice must survive untouched — the rewrite has to be invisible
// whenever there is data, or it is not a fix, it is a second bug.
func TestNonEmptySliceIsUnchanged(t *testing.T) {
	t.Parallel()

	f, buf := captureFormatter("json")
	if err := f.JSON([]nilSliceRow{{Name: "a"}, {Name: "b"}}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var out []nilSliceRow
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("payload changed: %+v", out)
	}
}

// A typed nil POINTER must not panic on the way through reflect.Elem().
func TestNilPointerDoesNotPanic(t *testing.T) {
	t.Parallel()

	var p *[]nilSliceRow
	f, buf := captureFormatter("json")
	if err := f.JSON(p); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "null" {
		t.Errorf("JSON(nil pointer) = %q, want %q", got, "null")
	}

	// A pointer TO a nil slice is a slice as far as the consumer is
	// concerned, so it gets the same treatment as the slice itself.
	var rows []nilSliceRow
	f, buf = captureFormatter("json")
	if err := f.JSON(&rows); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("JSON(&nilSlice) = %q, want %q", got, "[]")
	}
}

// NDJSON's contract for an empty list is zero lines, not a `null` line — one
// record per line means no records means nothing at all.
func TestNDJSONNilSliceEmitsNothing(t *testing.T) {
	t.Parallel()

	var rows []nilSliceRow
	f, buf := captureFormatter("ndjson")
	if err := f.NDJSON(rows); err != nil {
		t.Fatalf("NDJSON: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("NDJSON(nil slice) wrote %q, want nothing", buf.String())
	}
}
