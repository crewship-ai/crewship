package api

import (
	"strings"
	"testing"
)

func TestMergeIssueRunInputs(t *testing.T) {
	stored := map[string]any{"service": "api", "severity": "low"}
	if err := mergeIssueRunInputs(strings.NewReader(`{"routine_inputs":{"severity":"high","summary":"HTTP 503"}}`), stored); err != nil {
		t.Fatal(err)
	}
	if stored["service"] != "api" || stored["severity"] != "high" || stored["summary"] != "HTTP 503" {
		t.Fatalf("merged inputs = %#v", stored)
	}
	for _, body := range []string{`{"routine_inputs":null}`, `{"routine_inputs":[]}`, `{"routine_inputs":`} {
		if err := mergeIssueRunInputs(strings.NewReader(body), map[string]any{}); err == nil {
			t.Errorf("accepted invalid body %q", body)
		}
	}
	if err := mergeIssueRunInputs(strings.NewReader(""), map[string]any{}); err != nil {
		t.Fatalf("legacy empty body: %v", err)
	}
}
