package main

import (
	"os"
	"strings"
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

func TestReadValueStdin_Boundary(t *testing.T) {
	const limit = 64 * 1024
	for _, tc := range []struct {
		suffix    string
		wantError bool
	}{
		{"", false}, {"\n", false}, {"\r\n", false}, {"\nX", true}, {"\r\nX", true}, {"\n\n", true}, {"X", true},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "stdin")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.WriteString(strings.Repeat("a", limit) + tc.suffix); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			orig := os.Stdin
			os.Stdin = f
			defer func() { os.Stdin = orig }()
			value, err := readValueStdin()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%v", err, tc.wantError)
			}
			if err == nil && len(value) != limit {
				t.Fatalf("value length=%d", len(value))
			}
		})
	}
}
