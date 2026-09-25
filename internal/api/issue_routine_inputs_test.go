package api

import (
	"errors"
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
	for _, tc := range []struct{ name, body string }{
		{name: "null routine_inputs", body: `{"routine_inputs":null}`},
		{name: "array routine_inputs", body: `{"routine_inputs":[]}`},
		{name: "truncated JSON", body: `{"routine_inputs":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := mergeIssueRunInputs(strings.NewReader(tc.body), map[string]any{}); err == nil {
				t.Errorf("accepted invalid body %q", tc.body)
			}
		})
	}
	if err := mergeIssueRunInputs(strings.NewReader(""), map[string]any{}); err != nil {
		t.Fatalf("legacy empty body: %v", err)
	}
	const wrapper = `{"routine_inputs":{"pad":""}}`
	if err := mergeIssueRunInputs(strings.NewReader(`{"routine_inputs":{"pad":"`+strings.Repeat("x", 1<<20-len(wrapper))+`"}}`), map[string]any{}); err != nil {
		t.Fatalf("body exactly 1 MiB: %v", err)
	}
	if err := mergeIssueRunInputs(strings.NewReader(`{"routine_inputs":{"pad":"`+strings.Repeat("x", 1<<20-len(wrapper)+1)+`"}}`), map[string]any{}); err == nil {
		t.Error("accepted a body one byte over 1 MiB")
	}
	if err := mergeIssueRunInputs(errReader{}, map[string]any{}); err == nil {
		t.Error("accepted a body whose reader fails")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
