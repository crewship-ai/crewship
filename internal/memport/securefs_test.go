package memport

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func linkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

// os.DirFS follows symlinks. Reading an agent's memory through it hands
// back whatever the agent pointed a .md at — another crew's memory, or
// any file the server process can read — inside a response the operator
// believes is scoped to one agent.
func TestSecureDirFS_DoesNotFollowFileSymlinks(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "other-crew.md")
	if err := os.WriteFile(outside, []byte("another crew's memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENT.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkOrSkip(t, outside, filepath.Join(root, "notes.md"))

	// Baseline: the standard FS does leak it, which is why this type exists.
	if b, err := fs.ReadFile(os.DirFS(root), "notes.md"); err == nil && string(b) == "another crew's memory" {
		t.Log("confirmed: os.DirFS follows the link")
	}

	fsys := SecureDirFS(root)
	if _, err := fs.ReadFile(fsys, "notes.md"); err == nil {
		t.Error("SecureDirFS read through a file symlink")
	}
	got, err := fs.ReadFile(fsys, "AGENT.md")
	if err != nil {
		t.Fatalf("regular file: %v", err)
	}
	if string(got) != "mine" {
		t.Errorf("AGENT.md = %q", got)
	}
}

// A symlinked directory must not be walked into, or the export walks
// out of the tree one level up instead of one file at a time.
func TestSecureDirFS_DoesNotWalkSymlinkedDirs(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "secret.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkOrSkip(t, elsewhere, filepath.Join(root, "daily"))
	if err := os.WriteFile(filepath.Join(root, "AGENT.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	names, err := walkFiles(SecureDirFS(root))
	if err != nil {
		t.Fatalf("walkFiles: %v", err)
	}
	for _, n := range names {
		if n == "daily/secret.md" {
			t.Fatalf("walked into a symlinked directory: %v", names)
		}
	}
	if len(names) != 1 || names[0] != "AGENT.md" {
		t.Errorf("names = %v, want [AGENT.md]", names)
	}
}

// The whole point is that an export of a tampered tree still works —
// it just excludes what it must not read.
func TestSecureDirFS_ExportStillReturnsTheRealFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("AGENT.md", "knowledge\n")
	mustWrite("daily/2026-08-01.md", "today\n")
	linkOrSkip(t, "/etc/passwd", filepath.Join(root, "pins.md"))

	plan, err := ReadSource(SecureDirFS(root), FormatCrewship, Options{})
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if len(plan.Docs) != 2 {
		t.Fatalf("Docs = %v, want the two real files", relPaths(plan))
	}
	for _, d := range plan.Docs {
		if d.RelPath == "pins.md" {
			t.Error("symlinked pins.md was exported")
		}
	}
}
