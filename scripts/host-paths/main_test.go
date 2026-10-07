package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseRatchetAllowsOnlyExistingDebt(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, b)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "Test")
	path := "internal/example/root.go"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	source := []byte("package example\nimport \"os\"\nfunc Root(){os.UserHomeDir()}\n")
	if err := os.WriteFile(filepath.Join(root, path), source, 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "existing root")
	keys, err := scan(path, source)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []exception{{keys[0], "existing debt"}}
	if err := checkBase(root, "HEAD", allowed); err != nil {
		t.Fatalf("initial baseline rejected: %v", err)
	}
	newKeys, err := scan(path, []byte("package example\nimport \"os\"\nfunc New(){os.TempDir()}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkBase(root, "HEAD", []exception{{newKeys[0], "new root"}}); err == nil {
		t.Fatal("new syntax accepted into initial baseline")
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts/host-paths"), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, allowlistPath), b, 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "baseline")
	if err := checkBase(root, "HEAD", append(allowed, exception{newKeys[0], "expansion"})); err == nil {
		t.Fatal("allowlist expansion accepted")
	}
	if err := checkBase(root, "HEAD", nil); err != nil {
		t.Fatalf("shrink rejected: %v", err)
	}
	if err := checkBase(root, "missing-revision", allowed); err == nil {
		t.Fatal("missing base accepted")
	}
}

func TestScanRecognizesAliasAndLiteral(t *testing.T) {
	keys, err := scan("internal/example/file.go", []byte("package example\nimport host \"os\"\nfunc Root() { host.UserHomeDir(); host.TempDir(); host.Getwd(); _ = \"/tmp/data\" }"))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 4 {
		t.Fatalf("got %v", keys)
	}
	if err := validate(keys, nil); err == nil || !strings.Contains(err.Error(), "new host path root") {
		t.Fatalf("new roots accepted: %v", err)
	}
	allowed := make([]exception, 0, len(keys))
	for _, k := range keys {
		allowed = append(allowed, exception{k, "existing container path or migration debt"})
	}
	if err := validate(keys, allowed); err != nil {
		t.Fatal(err)
	}
	if err := validate(keys[:1], allowed); err == nil || !strings.Contains(err.Error(), "stale exception") {
		t.Fatalf("stale debt accepted: %v", err)
	}
}

func TestScanDoesNotMatchCommentsAndOtherPackages(t *testing.T) {
	keys, err := scan("internal/example/file.go", []byte("package example\n// os.TempDir() /tmp/data\nfunc F() { other.TempDir(); _ = \"docs /tmp/data\" }"))
	if err != nil || len(keys) != 0 {
		t.Fatalf("got %v %v", keys, err)
	}
	if productFile("internal/example/testdata/source.go") || productFile("internal/example/file_test.go") {
		t.Fatal("fixtures included")
	}
	if !productFile("internal/example/file.go") {
		t.Fatal("production code excluded")
	}
}
