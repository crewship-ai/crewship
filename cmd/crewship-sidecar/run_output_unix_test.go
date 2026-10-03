//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunOutputCommandsRetainResultAndReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capture")
	argsFile := filepath.Join(filepath.Dir(dir), "args")
	if err := os.WriteFile(argsFile, []byte("sh\x00-c\x00printf retained; exit 7\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := runOutputCommand(t.Context(), "run-capture", []string{"--dir", dir, "--args-file", argsFile}, &out, &diagnostics); code != 7 {
		t.Fatalf("capture=%d %s", code, diagnostics.String())
	}
	if out.Len() != 0 {
		t.Fatal("capture depends on a live stdout consumer")
	}
	if code := runOutputCommand(t.Context(), "run-read", []string{"--dir", dir, "--raw"}, &out, &diagnostics); code != 7 || out.String() != "retained" {
		t.Fatalf("read=%d out=%q errors=%s", code, out.String(), diagnostics.String())
	}
	out.Reset()
	if code := runOutputCommand(t.Context(), "run-read", []string{"--dir", dir, "--after", "1"}, &out, &diagnostics); code != 0 || !strings.Contains(out.String(), `"reason":"exited"`) {
		t.Fatalf("events=%d %q", code, out.String())
	}
}
