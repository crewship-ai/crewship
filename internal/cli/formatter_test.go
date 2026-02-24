package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatterJSON(t *testing.T) {
	var buf bytes.Buffer
	f := &Formatter{Format: "json", Writer: &buf}

	data := []map[string]string{
		{"name": "alice", "role": "admin"},
		{"name": "bob", "role": "user"},
	}
	if err := f.JSON(data); err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("got %d items, want 2", len(parsed))
	}
	if parsed[0]["name"] != "alice" {
		t.Errorf("first name = %q, want alice", parsed[0]["name"])
	}
}

func TestFormatterYAML(t *testing.T) {
	var buf bytes.Buffer
	f := &Formatter{Format: "yaml", Writer: &buf}

	data := map[string]string{"key": "value"}
	if err := f.YAML(data); err != nil {
		t.Fatalf("YAML() error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "key: value") {
		t.Errorf("YAML output missing expected content: %s", out)
	}
}

func TestFormatterTable(t *testing.T) {
	// Disable colors for testing
	oldBold, oldReset := Bold, Reset
	Bold, Reset = "", ""
	defer func() { Bold, Reset = oldBold, oldReset }()

	var buf bytes.Buffer
	f := &Formatter{Format: "table", Writer: &buf}

	headers := []string{"NAME", "ROLE"}
	rows := [][]string{
		{"alice", "admin"},
		{"bob", "user"},
	}
	f.Table(headers, rows)

	out := buf.String()
	if !strings.Contains(out, "NAME") {
		t.Errorf("table missing header NAME: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("table missing row alice: %s", out)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("table missing row bob: %s", out)
	}
}

func TestFormatterQuiet(t *testing.T) {
	var buf bytes.Buffer
	f := &Formatter{Format: "quiet", Writer: &buf}

	rows := [][]string{
		{"alice", "admin"},
		{"bob", "user"},
	}
	f.Table(nil, rows)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0] != "alice" {
		t.Errorf("line 0 = %q, want alice", lines[0])
	}
	if lines[1] != "bob" {
		t.Errorf("line 1 = %q, want bob", lines[1])
	}
}

func TestFormatterDetail(t *testing.T) {
	oldBold, oldReset := Bold, Reset
	Bold, Reset = "", ""
	defer func() { Bold, Reset = oldBold, oldReset }()

	var buf bytes.Buffer
	f := &Formatter{Format: "table", Writer: &buf}

	pairs := [][]string{
		{"Name", "alice"},
		{"Role", "admin"},
	}
	f.Detail(pairs)

	out := buf.String()
	if !strings.Contains(out, "Name:") {
		t.Errorf("detail missing Name: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("detail missing alice: %s", out)
	}
}

func TestAutoRoutesCorrectly(t *testing.T) {
	data := []string{"a", "b"}
	headers := []string{"COL"}
	rows := [][]string{{"a"}, {"b"}}

	tests := []struct {
		format   string
		contains string
	}{
		{"json", `"a"`},
		{"yaml", "- a"},
		{"quiet", "a\nb"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			var buf bytes.Buffer
			f := &Formatter{Format: tt.format, Writer: &buf}
			if err := f.Auto(data, headers, rows); err != nil {
				t.Fatalf("Auto() error: %v", err)
			}
			if !strings.Contains(buf.String(), tt.contains) {
				t.Errorf("Auto(%s) = %q, want to contain %q", tt.format, buf.String(), tt.contains)
			}
		})
	}
}

func TestAutoDetailRoutesCorrectly(t *testing.T) {
	data := map[string]string{"name": "alice"}
	pairs := [][]string{{"Name", "alice"}}

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		f := &Formatter{Format: "json", Writer: &buf}
		if err := f.AutoDetail(data, pairs); err != nil {
			t.Fatalf("AutoDetail() error: %v", err)
		}
		if !strings.Contains(buf.String(), `"name"`) {
			t.Errorf("missing name in JSON: %s", buf.String())
		}
	})

	t.Run("quiet", func(t *testing.T) {
		var buf bytes.Buffer
		f := &Formatter{Format: "quiet", Writer: &buf}
		if err := f.AutoDetail(data, pairs); err != nil {
			t.Fatalf("AutoDetail() error: %v", err)
		}
		if strings.TrimSpace(buf.String()) != "alice" {
			t.Errorf("quiet output = %q, want alice", buf.String())
		}
	})
}
