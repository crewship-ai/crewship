package main

import (
	"os"
	"testing"
)

// #2428: a multi-line value (Codex auth.json, a PEM key) must arrive whole.
func TestReadValueStdin_KeepsEveryLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })

	const value = "{\n  \"tokens\": {\n    \"access_token\": \"a\"\n  }\n}\n"
	go func() {
		_, _ = w.WriteString(value)
		_ = w.Close()
	}()

	got, err := readValueStdin()
	if err != nil {
		t.Fatalf("readValueStdin: %v", err)
	}
	if got != "{\n  \"tokens\": {\n    \"access_token\": \"a\"\n  }\n}" {
		t.Errorf("got %q", got)
	}
}
