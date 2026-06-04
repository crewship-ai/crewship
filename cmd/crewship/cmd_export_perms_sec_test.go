package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSecExportWriteJSONFilePerms asserts the export bundle's JSON writer
// (run.json / messages.json / journal.json) creates files with 0600, so
// the full prompts/responses/journal it contains are not world-readable.
func TestSecExportWriteJSONFilePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")

	if err := writeJSONFile(path, map[string]any{"k": "v"}); err != nil {
		t.Fatalf("writeJSONFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("run.json mode = %#o, want no group/other bits (0600)", mode)
	}
}

// TestSecExportArtifactWriterPerms asserts the plain-text artifact writer
// (prompt.md / response.md / timeline.txt) and the output directory it
// lives in are created with 0600 / 0700 respectively.
func TestSecExportArtifactWriterPerms(t *testing.T) {
	base := t.TempDir()
	out := filepath.Join(base, "run-r_sec")

	if err := exportMkdir(out); err != nil {
		t.Fatalf("exportMkdir: %v", err)
	}
	di, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := di.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("output dir mode = %#o, want no group/other bits (0700)", mode)
	}

	for _, name := range []string{"prompt.md", "response.md", "timeline.txt"} {
		path := filepath.Join(out, name)
		if err := writeArtifactFile(path, []byte("sensitive")); err != nil {
			t.Fatalf("writeArtifactFile %s: %v", name, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if mode := fi.Mode().Perm(); mode&0o077 != 0 {
			t.Fatalf("%s mode = %#o, want no group/other bits (0600)", name, mode)
		}
	}
}
