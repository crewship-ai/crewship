package api

import (
	"errors"
	"strings"
	"testing"
)

func TestReadAndMergeIssueRunInputs(t *testing.T) {
	run := readIssueRunInputs(strings.NewReader(`{"routine_inputs":{"severity":"high","summary":"HTTP 503"}}`))
	if run.err != nil {
		t.Fatal(run.err)
	}
	stored := map[string]any{"service": "api", "severity": "low"}
	mergeIssueRunInputs(stored, run)
	if stored["service"] != "api" || stored["severity"] != "high" || stored["summary"] != "HTTP 503" {
		t.Fatalf("merged inputs = %#v", stored)
	}
	for _, tc := range []struct{ name, body string }{
		{name: "null routine_inputs", body: `{"routine_inputs":null}`},
		{name: "array routine_inputs", body: `{"routine_inputs":[]}`},
		{name: "truncated JSON", body: `{"routine_inputs":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if run := readIssueRunInputs(strings.NewReader(tc.body)); run.err == nil {
				t.Errorf("accepted invalid body %q", tc.body)
			}
		})
	}
	if run := readIssueRunInputs(strings.NewReader("")); run.err != nil || run.values != nil {
		t.Fatalf("legacy empty body: %+v", run)
	}
	const wrapper = `{"routine_inputs":{"pad":""}}`
	if run := readIssueRunInputs(strings.NewReader(`{"routine_inputs":{"pad":"` + strings.Repeat("x", 1<<20-len(wrapper)) + `"}}`)); run.err != nil {
		t.Fatalf("body exactly 1 MiB: %v", run.err)
	}
	if run := readIssueRunInputs(strings.NewReader(`{"routine_inputs":{"pad":"` + strings.Repeat("x", 1<<20-len(wrapper)+1) + `"}}`)); run.err == nil {
		t.Error("accepted a body one byte over 1 MiB")
	}
	if run := readIssueRunInputs(errReader{}); run.err == nil {
		t.Error("accepted a body whose reader fails")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
