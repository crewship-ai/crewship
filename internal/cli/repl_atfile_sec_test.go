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

// TestSecAtFileSymlinkEscapeRejected pins the symlink-resolving containment:
// an in-tree symlink that points OUTSIDE the working directory must not let
// a `@file` read smuggle out-of-tree content past the lexical prefix check.
func TestSecAtFileSymlinkEscapeRejected(t *testing.T) {
	parent := t.TempDir()
	work := filepath.Join(parent, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(parent, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink inside work pointing at the out-of-tree secret file.
	if err := os.Symlink(secret, filepath.Join(work, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A symlink inside work pointing at the out-of-tree parent directory.
	if err := os.Symlink(parent, filepath.Join(work, "out")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	restore := chdir(t, work)
	defer restore()

	for _, p := range []string{"link.txt", filepath.FromSlash("out/secret.txt")} {
		data, err := readAtFileBounded(p, false)
		if err == nil && strings.Contains(string(data), "TOP-SECRET") {
			t.Fatalf("symlink escape %q leaked out-of-tree content: %q", p, string(data))
		}
	}

	// A filename merely CONTAINING ".." (not a traversal segment) is allowed.
	if err := os.WriteFile(filepath.Join(work, "release..md"), []byte("OK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := readAtFileBounded("release..md", false); err != nil || string(data) != "OK" {
		t.Errorf("`release..md` should be readable; got %q, %v", string(data), err)
	}
}

// chdir switches into dir for the test using t.Chdir, which isolates the
// change and auto-restores at test end (no process-global cwd leak). The
// returned func is a no-op kept for call-site compatibility.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	t.Chdir(dir)
	return func() {}
}
