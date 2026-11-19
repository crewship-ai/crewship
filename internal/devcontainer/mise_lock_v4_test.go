package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #5545: mise lockfile v4 writes npm/aube auxiliaries next to a global lock as
// `locks/<tool>/<version~hash>/…` and references that relative path from
// mise.lock. Older locks use `.mise/locks/…`. Both layouts are the native
// bundle; anything else stays rejected.
func TestMiseLockV4AuxiliaryPaths(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"mise.lock", true},
		{".mise/locks/gemini-cli/1/package.json", true},
		{"locks/opencode/2.3.0~009061c2/aube-lock.yaml", true},
		{"locks/opencode/2.3.0~009061c2/package.json", true},
		{"locks/opencode/package.json", true},
		{"locks/package.json", false},
		{"locks/../mise.lock", false},
		{"locks/./opencode/package.json", false},
		{"locks//opencode/package.json", false},
		{"lockz/opencode/1/package.json", false},
		{"config.toml", false},
		{"locks/opencode/1/pack age.json", false},
	} {
		if got := validMiseLockPath(tc.path); got != tc.want {
			t.Errorf("validMiseLockPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestResolverArchiveAcceptsLockfileV4Auxiliaries(t *testing.T) {
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	files := []struct{ name, body string }{
		{"config/config.toml", "[tools]\n"},
		{"config/mise.lock", "lockfile_version = 4\n"},
		{"config/locks/opencode/2.3.0~009061c2/package.json", "{}"},
		{"config/locks/opencode/2.3.0~009061c2/aube-lock.yaml", "lock: true\n"},
	}
	for _, dir := range []string{"config/", "config/locks/", "config/locks/opencode/", "config/locks/opencode/2.3.0~009061c2/"} {
		if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Typeflag: tar.TypeReg, Size: int64(len(f.body)), Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := readResolverLockArchive(&data)
	if err != nil {
		t.Fatalf("native v4 resolver output rejected: %v", err)
	}
	if len(b.Files) != 3 || b.Files["locks/opencode/2.3.0~009061c2/package.json"] != "{}" || b.Files["locks/opencode/2.3.0~009061c2/aube-lock.yaml"] != "lock: true\n" {
		t.Fatalf("bundle = %+v", b.Files)
	}
	if _, leaked := b.Files["config.toml"]; leaked {
		t.Fatal("resolver config leaked into bundle")
	}
}

func TestReadMiseLockBundleIncludesLockfileV4Auxiliaries(t *testing.T) {
	dir := t.TempDir()
	lock := "lockfile_version = 4\n\n[[tools.opencode]]\nversion = \"2.3.0\"\naube = { path = \"locks/opencode/2.3.0~009061c2\", digest = \"sha256:00\" }\n"
	for name, data := range map[string]string{"mise.lock": lock, "locks/opencode/2.3.0~009061c2/package.json": "{}", "locks/opencode/2.3.0~009061c2/aube-lock.yaml": "lock: true\n", "locks/project-owned/notes.txt": "UNRELATED_PROJECT_FILE", "config.toml": "EXAMPLE_PRIVATE_ENV"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadMiseLockBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 3 || got.Files["locks/opencode/2.3.0~009061c2/aube-lock.yaml"] != "lock: true\n" {
		t.Fatalf("bundle = %+v", got.Files)
	}
	for _, data := range got.Files {
		if strings.Contains(data, "UNRELATED_PROJECT_FILE") || strings.Contains(data, "EXAMPLE_PRIVATE_ENV") {
			t.Fatal("an unreferenced project file entered the bundle")
		}
	}
}

func TestReadMiseLockBundleRejectsLinkedV4Directory(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "opencode", "package.json"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mise.lock"), []byte("lockfile_version = 4\naube = { path = \"locks/opencode\" }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "locks")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if b, err := ReadMiseLockBundle(dir); err == nil {
		t.Fatalf("linked locks directory accepted: %+v", b.Files)
	}
}

// A rebuild must not inherit auxiliary files from a previous bundle in either
// layout, and both cleanup targets are checked against inherited symlinks.
func TestMiseLockWriteCleansBothAuxiliaryLayouts(t *testing.T) {
	var scripts []string
	b := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "lockfile_version = 4\n", "locks/opencode/1/package.json": "{}"}}
	err := writeMiseLock(context.Background(), "builder", b, func(_ context.Context, _ string, args []string, _ string, _ []string) (string, int, error) {
		scripts = append(scripts, strings.Join(args, " "))
		return "", 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := scripts[0]
	for _, want := range []string{"test ! -L /opt/mise/config/.mise;", "rm -rf /opt/mise/config/.mise/locks;", "test ! -L /opt/mise/config/locks;", "rm -rf /opt/mise/config/locks;"} {
		if !strings.Contains(cleanup, want) {
			t.Errorf("cleanup %q lacks %q", cleanup, want)
		}
	}
	joined := strings.Join(scripts, "\n")
	if !strings.Contains(joined, "test ! -L '/opt/mise/config/locks'; ") || !strings.Contains(joined, ": > '/opt/mise/config/locks/opencode/1/package.json'") {
		t.Fatalf("v4 auxiliary not written at its relative path with parent checks:\n%s", joined)
	}
}

func TestReadMiseLockBundleV4ReferencesFailClosed(t *testing.T) {
	write := func(t *testing.T, dir string, files map[string]string) {
		t.Helper()
		for name, data := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	ref := func(p string) string { return "lockfile_version = 4\naube = { path = \"" + p + "\" }\n" }

	t.Run("nested files inside a referenced directory are packed", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"mise.lock": ref("locks/opencode/1"), "locks/opencode/1/package.json": "{}", "locks/opencode/1/sub/extra.yaml": "x: 1\n"})
		got, err := ReadMiseLockBundle(dir)
		if err != nil || got.Files["locks/opencode/1/sub/extra.yaml"] != "x: 1\n" {
			t.Fatalf("nested auxiliary dropped: %v %+v", err, got)
		}
	})
	t.Run("missing referenced directory", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"mise.lock": ref("locks/opencode/1")})
		if _, err := ReadMiseLockBundle(dir); err == nil {
			t.Fatal("incomplete bundle accepted")
		}
	})
	t.Run("empty referenced directory", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"mise.lock": ref("locks/opencode/1")})
		if err := os.MkdirAll(filepath.Join(dir, "locks/opencode/1"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMiseLockBundle(dir); err == nil {
			t.Fatal("empty reference accepted")
		}
	})
	t.Run("reference naming a file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"mise.lock": ref("locks/opencode/package.json"), "locks/opencode/package.json": "{}"})
		if _, err := ReadMiseLockBundle(dir); err == nil {
			t.Fatal("file reference accepted")
		}
	})
	t.Run("traversal reference", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"mise.lock": ref("locks/../config"), "config/x": "x"})
		if _, err := ReadMiseLockBundle(dir); err == nil {
			t.Fatal("traversal reference accepted")
		}
	})
	t.Run("symlinked referenced directory", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		write(t, outside, map[string]string{"package.json": "outside"})
		write(t, dir, map[string]string{"mise.lock": ref("locks/opencode/1")})
		if err := os.MkdirAll(filepath.Join(dir, "locks/opencode"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "locks/opencode/1")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := ReadMiseLockBundle(dir); err == nil {
			t.Fatal("symlinked reference accepted")
		}
	})
	t.Run("unrelated locks tree is never opened for a v3 lock", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"mise.lock": "lockfile_version = 3\n", ".mise/locks/node/1/package.json": "{}", "locks/" + strings.Repeat("a", 250) + "/" + strings.Repeat("b", 20): "project"})
		got, err := ReadMiseLockBundle(dir)
		if err != nil || len(got.Files) != 2 {
			t.Fatalf("unrelated locks/ tree affected v3 packing: %v %+v", err, got)
		}
	})
	t.Run("locks as a file is ignored when unreferenced", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"mise.lock": "lockfile_version = 3\n", "locks": "project file"})
		if _, err := ReadMiseLockBundle(dir); err != nil {
			t.Fatalf("unreferenced locks file broke packing: %v", err)
		}
	})
}

func TestResolverArchiveRejectsNonLockV4Neighbours(t *testing.T) {
	for _, name := range []string{"config/conf.d/x.toml", "config/locks/x", "config/locks/opencode/../../mise.toml"} {
		var data bytes.Buffer
		tw := tar.NewWriter(&data)
		for _, f := range []string{"config/mise.lock", name} {
			if err := tw.WriteHeader(&tar.Header{Name: f, Typeflag: tar.TypeReg, Size: 1, Mode: 0o600}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(tw, "x"); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if b, err := readResolverLockArchive(&data); err == nil {
			t.Errorf("%s accepted: %+v", name, b.Files)
		}
	}
}
