package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecAtFileTraversalRejected pins the containment guard on
// readAtFileBounded: a relative @file token that climbs out of the
// working directory with ".." must be refused, so a prompt like
// `@../../etc/hosts` cannot inline arbitrary host files.
//
// We chdir into a temp dir (with t.Cleanup restoring cwd) and place a
// sentinel file *outside* it; the relative escaping path must error.
func TestSecAtFileTraversalRejected(t *testing.T) {
	parent := t.TempDir()
	work := filepath.Join(parent, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// Sentinel one level above the working dir — the file genuinely
	// exists, so a passing read would prove the containment is broken
	// (not just that the path was missing).
	secret := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	restore := chdir(t, work)
	defer restore()

	escaping := []string{
		"../secret.txt",
		"../../etc/hosts",
		filepath.FromSlash("a/../../secret.txt"),
	}
	for _, p := range escaping {
		data, err := readAtFileBounded(p, false)
		if err == nil {
			t.Errorf("readAtFileBounded(%q) = %q, nil; want containment error", p, string(data))
		}
		if strings.Contains(string(data), "TOP-SECRET") {
			t.Errorf("readAtFileBounded(%q) leaked out-of-tree file content", p)
		}
	}
}

// TestSecAtFileAbsoluteRejected ensures an absolute path that escapes
// the working directory is refused too (the @file surface is for
// in-repo relative references, not arbitrary filesystem reads).
func TestSecAtFileAbsoluteRejected(t *testing.T) {
	parent := t.TempDir()
	work := filepath.Join(parent, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}

	restore := chdir(t, work)
	defer restore()

	if data, err := readAtFileBounded(outside, false); err == nil {
		t.Errorf("readAtFileBounded(abs outside cwd) = %q, nil; want error", string(data))
	}
}

// TestSecAtFileInDirAllowed is the green-path guard: a legitimate
// relative @file read inside the working directory must keep working
// after the containment fix.
func TestSecAtFileInDirAllowed(t *testing.T) {
	work := t.TempDir()
	want := "hello from in-dir file"
	if err := os.WriteFile(filepath.Join(work, "notes.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	// A nested file reached via a clean relative path is fine.
	if err := os.Mkdir(filepath.Join(work, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "sub", "child.txt"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := chdir(t, work)
	defer restore()

	data, err := readAtFileBounded("notes.md", false)
	if err != nil {
		t.Fatalf("readAtFileBounded(in-dir) errored: %v", err)
	}
	if string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}

	if data, err := readAtFileBounded(filepath.FromSlash("sub/child.txt"), false); err != nil || string(data) != "child" {
		t.Errorf("nested in-dir read = %q, %v; want %q, nil", string(data), err, "child")
	}
}

// chdir switches into dir and returns a restore func. Kept local so the
// security test doesn't depend on test helpers in sibling files.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}
