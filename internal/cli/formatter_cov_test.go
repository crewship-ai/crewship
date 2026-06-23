package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNDJSON_NilIsNoOp(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	if err := f.NDJSON(nil); err != nil {
		t.Fatalf("NDJSON(nil): %v", err)
	}
	if b.Len() != 0 {
		t.Errorf("NDJSON(nil) wrote %q, want nothing", b.String())
	}
}

func TestNDJSON_ByteSliceIsSingleRecord(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	if err := f.NDJSON([]byte("abc")); err != nil {
		t.Fatalf("NDJSON([]byte): %v", err)
	}
	out := strings.TrimSpace(b.String())
	if strings.Contains(out, "\n") {
		t.Errorf("[]byte must encode as ONE line, got %q", out)
	}
	// encoding/json represents []byte as a base64 string ("abc" → "YWJj").
	if out != `"YWJj"` {
		t.Errorf("got %q, want base64 JSON string \"YWJj\"", out)
	}
}

func TestNDJSON_ElementEncodeError(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	if err := f.NDJSON([]interface{}{"fine", make(chan int)}); err == nil {
		t.Error("want error for unmarshalable slice element")
	}
}

func TestNDJSON_ScalarEncodeError(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	if err := f.NDJSON(make(chan int)); err == nil {
		t.Error("want error for unmarshalable scalar")
	}
}

func TestAuto_DefaultFallsBackToTable(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "table", Writer: &b}
	if err := f.Auto([]string{"a"}, []string{"NAME"}, [][]string{{"alice"}}); err != nil {
		t.Fatalf("Auto: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "alice") {
		t.Errorf("table output missing header/row: %q", out)
	}
}

func TestAuto_QuietSkipsEmptyRows(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "quiet", Writer: &b}
	if err := f.Auto(nil, nil, [][]string{{}, {"id-1"}, {}}); err != nil {
		t.Fatalf("Auto: %v", err)
	}
	if strings.TrimSpace(b.String()) != "id-1" {
		t.Errorf("quiet output = %q, want only id-1 (empty rows skipped)", b.String())
	}
}

func TestAutoDetail_YAML(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "yaml", Writer: &b}
	if err := f.AutoDetail(map[string]string{"name": "alice"}, [][]string{{"Name", "alice"}}); err != nil {
		t.Fatalf("AutoDetail yaml: %v", err)
	}
	if !strings.Contains(b.String(), "name: alice") {
		t.Errorf("yaml output = %q", b.String())
	}
}

func TestAutoDetail_NDJSON(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "ndjson", Writer: &b}
	if err := f.AutoDetail(map[string]string{"id": "x1"}, nil); err != nil {
		t.Fatalf("AutoDetail ndjson: %v", err)
	}
	out := strings.TrimSpace(b.String())
	if out != `{"id":"x1"}` {
		t.Errorf("ndjson detail = %q, want single compact JSON line", out)
	}
}

func TestAutoDetail_DefaultUsesDetail(t *testing.T) {
	oldBold, oldReset := Bold, Reset
	Bold, Reset = "", ""
	defer func() { Bold, Reset = oldBold, oldReset }()

	var b bytes.Buffer
	f := &Formatter{Format: "table", Writer: &b}
	if err := f.AutoDetail(nil, [][]string{{"Name", "alice"}, {"Role", "admin"}}); err != nil {
		t.Fatalf("AutoDetail default: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "Name:") || !strings.Contains(out, "admin") {
		t.Errorf("detail output = %q", out)
	}
}

func TestAutoDetail_QuietEmptyPairs(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "quiet", Writer: &b}
	if err := f.AutoDetail(nil, nil); err != nil {
		t.Fatalf("AutoDetail quiet empty: %v", err)
	}
	if b.Len() != 0 {
		t.Errorf("quiet with no pairs wrote %q, want nothing", b.String())
	}
}

func TestAutoDetail_QuietShortPairSkipped(t *testing.T) {
	var b bytes.Buffer
	f := &Formatter{Format: "quiet", Writer: &b}
	// First pair has fewer than 2 elements → nothing printable.
	if err := f.AutoDetail(nil, [][]string{{"only-key"}}); err != nil {
		t.Fatalf("AutoDetail: %v", err)
	}
	if b.Len() != 0 {
		t.Errorf("short pair should print nothing, got %q", b.String())
	}
}
