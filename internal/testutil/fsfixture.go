// Package testutil provides shared testing helpers for the Crewship codebase.
//
// The fsfixture helpers use spf13/afero to build in-memory filesystem
// fixtures for tests. This lets tests avoid touching real disk, which gives:
//
//   - Much faster test runs (no I/O syscalls)
//   - No temp-dir cleanup needed
//   - Perfect isolation (no interference between parallel tests)
//   - Deterministic behavior (no leftover files from previous runs)
//
// Example usage:
//
//	func TestMyThing(t *testing.T) {
//	    fs := testutil.NewMemFS(t, map[string]string{
//	        ".memory/AGENT.md":   "# My agent\nI remember stuff.",
//	        ".memory/notes/a.md": "note content",
//	    })
//	    // fs is a fully populated in-memory filesystem ready for assertions
//	    content, _ := afero.ReadFile(fs, ".memory/AGENT.md")
//	    assert.Contains(t, string(content), "I remember stuff")
//	}
package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// NewMemFS creates an in-memory filesystem pre-populated with the given files.
// Keys are file paths (relative to root), values are file contents.
// Parent directories are created automatically. Returns the afero.Fs for further
// manipulation by the test.
//
// The fs is automatically garbage-collected when the test ends — no cleanup
// is required by callers.
func NewMemFS(t *testing.T, files map[string]string) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()

	for path, content := range files {
		dir := filepath.Dir(path)
		if dir != "." && dir != "/" {
			if err := fs.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("testutil.NewMemFS: mkdir %s: %v", dir, err)
			}
		}
		if err := afero.WriteFile(fs, path, []byte(content), 0o644); err != nil {
			t.Fatalf("testutil.NewMemFS: write %s: %v", path, err)
		}
	}

	return fs
}

// AssertFileContent asserts that the file at path exists and its content
// contains the given substring. Uses t.Fatalf so the test stops on first miss.
func AssertFileContent(t *testing.T, fs afero.Fs, path, wantSubstring string) {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("AssertFileContent: read %s: %v", path, err)
	}
	if !contains(string(data), wantSubstring) {
		t.Fatalf("AssertFileContent: %s does not contain %q\ncontent: %s",
			path, wantSubstring, string(data))
	}
}

// ListFiles walks the filesystem rooted at the given path and returns all
// file paths (not directories). Useful for asserting which files exist in
// a fixture after an operation.
func ListFiles(t *testing.T, fs afero.Fs, root string) []string {
	t.Helper()
	var paths []string
	err := afero.Walk(fs, root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListFiles: walk %s: %v", root, err)
	}
	return paths
}

// contains wraps strings.Contains with a nil-safe shortcut.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
