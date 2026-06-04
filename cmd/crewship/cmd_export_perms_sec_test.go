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
		name := name
		t.Run(name, func(t *testing.T) {
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
		})
	}
}

// TestSecExportTightensPreExistingPerms verifies the writers TIGHTEN an
// already-present dir/file that was created world-readable by an older run
// — not just newly-created paths (CodeRabbit #612). exportMkdir must clamp
// a reused 0755 --out dir to 0700, and writeArtifactFile must clamp a
// pre-existing 0644 artifact to 0600.
func TestSecExportTightensPreExistingPerms(t *testing.T) {
	base := t.TempDir()

	// Pre-existing loose directory.
	out := filepath.Join(base, "stale-bundle")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("seed loose dir: %v", err)
	}
	if err := exportMkdir(out); err != nil {
		t.Fatalf("exportMkdir: %v", err)
	}
	if di, _ := os.Stat(out); di.Mode().Perm()&0o077 != 0 {
		t.Fatalf("reused dir mode = %#o, want tightened to 0700", di.Mode().Perm())
	}

	// Pre-existing world-readable artifact.
	path := filepath.Join(out, "run.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed loose file: %v", err)
	}
	if err := writeArtifactFile(path, []byte("new")); err != nil {
		t.Fatalf("writeArtifactFile: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("reused file mode = %#o, want tightened to 0600", fi.Mode().Perm())
	}
}
