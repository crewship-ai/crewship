package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

// #1423 item 2: `routine validate` / `routine save` accept YAML as an
// input format (comments + real multiline strings — no more smuggling
// instructions into `description` as a JSON-escape workaround). ToCanonicalJSON
// is the shared conversion both CLI entry points call before Parse/Validate
// and before the definition bytes go over the wire — the server, DB, and
// every other Parse call site keep seeing canonical JSON, unchanged.

func TestToCanonicalJSON_PassesThroughJSONUnchanged(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"name":"demo","steps":[]}`,
		"  \n\t{\"name\":\"demo\"}",        // leading whitespace before JSON
		`[1,2,3]`,                          // JSON array (not a valid DSL doc, but still JSON — Parse rejects it downstream, not this layer)
		"\xEF\xBB\xBF" + `{"name":"demo"}`, // UTF-8 BOM + JSON
	}
	for _, in := range cases {
		got, err := ToCanonicalJSON([]byte(in))
		if err != nil {
			t.Errorf("input %q: unexpected error: %v", in, err)
			continue
		}
		if !json.Valid(got) {
			t.Errorf("input %q: output not valid JSON: %s", in, got)
		}
	}
}

func TestToCanonicalJSON_ConvertsYAML(t *testing.T) {
	t.Parallel()
	yamlSrc := `
dsl_version: "1.0"
name: yaml-routine
description: |
  A multiline description
  spanning two lines.
inputs:
  - name: since
    type: string
    required: false
    default: yesterday
steps:
  - id: fetch
    type: agent_run
    agent_slug: email-reader
    # comment: fetch prompt is a real multiline block, no JSON-escaping needed
    prompt: |
      Fetch emails since {{ inputs.since }}.
      Summarize the count.
`
	got, err := ToCanonicalJSON([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !json.Valid(got) {
		t.Fatalf("output not valid JSON: %s", got)
	}

	dsl, err := Parse(got)
	if err != nil {
		t.Fatalf("parse converted JSON: %v", err)
	}
	if dsl.Name != "yaml-routine" {
		t.Errorf("name: got %q", dsl.Name)
	}
	if len(dsl.Steps) != 1 || dsl.Steps[0].AgentSlug != "email-reader" {
		t.Fatalf("steps: got %+v", dsl.Steps)
	}
	if !strings.Contains(dsl.Steps[0].Prompt, "Fetch emails since {{ inputs.since }}.\nSummarize the count.") {
		t.Errorf("multiline prompt not preserved: %q", dsl.Steps[0].Prompt)
	}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Errorf("converted DSL should validate: %v", err)
	}
}

func TestToCanonicalJSON_RejectsMalformedYAML(t *testing.T) {
	t.Parallel()
	bad := "name: [unterminated"
	if _, err := ToCanonicalJSON([]byte(bad)); err == nil {
		t.Error("expected an error for malformed YAML")
	}
}

func TestToCanonicalJSON_EmptyInput(t *testing.T) {
	t.Parallel()
	if _, err := ToCanonicalJSON(nil); err == nil {
		t.Error("expected an error for empty input")
	}
	if _, err := ToCanonicalJSON([]byte("   \n\t")); err == nil {
		t.Error("expected an error for whitespace-only input")
	}
}

func TestLooksLikeJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{`{"a":1}`, true},
		{`[1,2]`, true},
		{"  \n{\"a\":1}", true},
		{"\xEF\xBB\xBF{\"a\":1}", true},
		{"name: demo", false},
		{"# a comment\nname: demo", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeJSON([]byte(tc.in)); got != tc.want {
			t.Errorf("looksLikeJSON(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
