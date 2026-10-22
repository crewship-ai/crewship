//go:build !windows

package appengine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A process the engine leaves behind must not outlive its execution: it could
// otherwise rewrite files between reading a candidate and checking it.
func TestEngineExecutionKillsItsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "engine.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n(sleep 1; echo late > late.txt) &\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runEngineProcess(t.Context(), script, dir, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "late.txt")); !os.IsNotExist(err) {
		t.Fatalf("engine descendant survived: %v", err)
	}
}
