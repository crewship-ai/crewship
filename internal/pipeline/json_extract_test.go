package pipeline

import (
	"strings"
	"testing"
)

// All four sites that decode LLM-emitted JSON (schema gate, outcomes
// grader, jsonPath template resolver, transform step) share this
// helper. The matrix below is the contract: every quirky shape we've
// seen in the wild must round-trip. Adding a new failure mode means
// adding a row here, not a per-site fix.

func TestExtractJSONCandidate_AcceptsLLMQuirks(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"bare object":               {`{"k":1}`, `{"k":1}`},
		"bare array":                {`[1,2,3]`, `[1,2,3]`},
		"fenced with json tag":      {"```json\n{\"k\":1}\n```", `{"k":1}`},
		"fenced upper-case":         {"```JSON\n{\"k\":1}\n```", `{"k":1}`},
		"fenced no tag":             {"```\n{\"k\":1}\n```", `{"k":1}`},
		"fenced with leading space": {"  ```json\n{\"k\":1}\n```\n", `{"k":1}`},
		"fenced unclosed":           {"```json\n{\"k\":1}", `{"k":1}`},
		"prose preamble":            {"Here you go:\n{\"k\":1}", `{"k":1}`},
		"markdown bullet preamble":  {"* Note: see below.\n{\"k\":1}", `{"k":1}`},
		// Trailing prose stays in the candidate; json.Decoder reads
		// one value and stops.
		"prose suffix": {"{\"k\":1}\nLet me know if you need anything.", "{\"k\":1}\nLet me know if you need anything."},
		"empty input":  {"", ""},
		"prose only":   {"nothing JSON-like here", "nothing JSON-like here"},
	}
	for name, tc := range cases {
		got := ExtractJSONCandidate(tc.in)
		if got != tc.want {
			t.Errorf("case %q: input=%q\n  got=%q\n want=%q", name, tc.in, got, tc.want)
		}
	}
}

func TestDecodeAgentJSON_RoundTripsQuirkyShapes(t *testing.T) {
	// The downstream sites all care about a single decode succeeding,
	// not which exact extractor branch fires. End-to-end round-trip
	// tests mirror that.
	cases := []string{
		`{"k":1}`,
		`[1,2,3]`,
		"```json\n{\"k\":1}\n```",
		"```\n[1,2]\n```",
		"Here you go:\n{\"k\":1}",
		"{\"k\":1}\nbye",
	}
	for _, raw := range cases {
		var out any
		if err := DecodeAgentJSON(raw, &out); err != nil {
			t.Errorf("raw=%q: unexpected decode error: %v", raw, err)
			continue
		}
		if out == nil {
			t.Errorf("raw=%q: decoded to nil", raw)
		}
	}
}

func TestDecodeAgentJSON_RejectsPureProse(t *testing.T) {
	// Regression guard: pure prose still has to fail. Otherwise the
	// validator can't tell "agent emitted garbage" from "agent emitted
	// JSON" and downstream consumers (schema gate, transform) lose
	// their failure signal.
	var out any
	err := DecodeAgentJSON("nothing here looks like JSON at all", &out)
	if err == nil {
		t.Fatal("expected error for pure prose input")
	}
	// Sanity-check: error mentions invalid character so callers can
	// surface a useful reason. This isn't a stable contract on the
	// exact wording, just a guard against silently succeeding.
	if !strings.Contains(err.Error(), "invalid") {
		t.Logf("note: error wording changed: %v", err)
	}
}
