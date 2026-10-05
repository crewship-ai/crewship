//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

type failingStream struct{ err error }

func (f failingStream) Read([]byte) (int, error)  { return 0, f.err }
func (f failingStream) Write([]byte) (int, error) { return 0, f.err }

func TestResponseStreamRejectsCorruptionAndIncompleteTermination(t *testing.T) {
	completion := `data: {"type":"response.completed","response":{"status":"completed"}}`
	for _, tc := range []struct{ name, input, want string }{
		{"invalid JSON", "data: {bad}\n\n", "invalid event"},
		{"unterminated invalid JSON", "data: {bad}", "invalid event"},
		{"duplicate completion", completion + "\n\n" + completion + "\n\n", "invalid completion"},
		{"noncompleted status", `data: {"type":"response.completed","response":{"status":"incomplete"}}` + "\n\n", "invalid completion"},
		{"provider error", `data: {"type":"error"}` + "\n\n", "provider incomplete"},
		{"incomplete", `data: {"type":"response.incomplete"}` + "\n\n", "provider incomplete"},
		{"aggregate response limit", strings.Repeat(": keepalive\n", 100000) + completion, "response limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := collectResponses(strings.NewReader(tc.input), &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want %s", err, tc.want)
			}
			if strings.Contains(out.String(), `"type":"done"`) {
				t.Fatalf("failure claimed completion: %s", out.String())
			}
		})
	}
	raw, err := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": strings.Repeat("x", (128<<10)+1)})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := collectResponses(strings.NewReader("data: "+string(raw)+"\n\n"), &out); err == nil || err.Error() != "output limit" || out.Len() != 0 {
		t.Fatalf("oversized text leaked: %v bytes=%d", err, out.Len())
	}
	if err := collectResponses(strings.NewReader("data: "+strings.Repeat("x", (1<<20)+1)), io.Discard); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestResponseStreamPreservesIOFailuresAndMultilineEvents(t *testing.T) {
	sentinel := errors.New("owned stream failure")
	if err := collectResponses(failingStream{sentinel}, io.Discard); !errors.Is(err, sentinel) {
		t.Fatalf("reader error lost: %v", err)
	}
	for _, input := range []string{
		`data: {"type":"response.output_text.delta","delta":"text"}` + "\n\n",
		`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n",
	} {
		if err := collectResponses(strings.NewReader(input), failingStream{sentinel}); !errors.Is(err, sentinel) {
			t.Fatalf("writer error lost: %v", err)
		}
	}
	var out bytes.Buffer
	input := ": comment\n\ndata: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"hello\"}\n\n" + `data: {"type":"response.completed","response":{"status":"completed"}}`
	if err := collectResponses(strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "{\"text\":\"hello\",\"type\":\"text\"}\n{\"type\":\"done\"}\n" {
		t.Fatalf("normalized output: %q", got)
	}
}

func TestRunnerRejectsUnsupportedModeAndUnexpectedArguments(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()
	for _, args := range [][]string{{"runner"}, {"runner", "unknown"}, {"runner", "hold", "extra"}, {"runner", "lease", "extra"}, {"runner", "broker", "extra"}, {"runner", "launch", "extra"}, {"runner", "responses"}} {
		os.Args = args
		if err := run(); err == nil {
			t.Fatalf("unexpected arguments accepted: %v", args)
		}
	}
	for _, body := range []string{"not JSON", strings.Repeat(" ", (128<<10)+1)} {
		if err := responses(context.Background(), body, io.Discard); err == nil || err.Error() != "request" {
			t.Fatalf("invalid request admitted: %v", err)
		}
	}
}
