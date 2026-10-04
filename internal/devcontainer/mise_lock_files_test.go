package devcontainer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMiseLockBundleIncludesAuxiliaryFilesOnly(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"mise.lock": "lockfile_version = 3\n", ".mise/locks/gemini-cli/1/package.json": "{}", ".mise/locks/gemini-cli/1/aube-lock.yaml": "test: true\n", "config.toml": "EXAMPLE_PRIVATE_ENV", "unrelated.txt": "not a lock"} {
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
	if len(got.Files) != 3 || got.Files[".mise/locks/gemini-cli/1/package.json"] != "{}" {
		t.Fatalf("bundle=%+v", got)
	}
	for _, data := range got.Files {
		if strings.Contains(data, "EXAMPLE_PRIVATE_ENV") {
			t.Fatal("configuration leaked into bundle")
		}
	}
}

func TestReadMiseLockBundleRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mise.lock"), []byte(strings.Repeat("x", maxMiseLockFileBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMiseLockBundle(dir); err == nil {
		t.Fatal("oversized lock accepted")
	}
}
