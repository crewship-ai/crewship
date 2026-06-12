package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrGenerate_NilLoggerUsesDefault(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	if err := LoadOrGenerate(context.Background(), dir, nil); err != nil {
		t.Fatalf("LoadOrGenerate with nil logger: %v", err)
	}
	// Secrets must still have been generated and persisted.
	vals, err := ReadPersisted(SecretsFilePath(dir))
	if err != nil {
		t.Fatalf("ReadPersisted: %v", err)
	}
	for _, m := range managed {
		if vals[m.EnvVar] == "" {
			t.Errorf("persisted %s missing after nil-logger bootstrap", m.EnvVar)
		}
	}
}

func TestLoadOrGenerate_SecretsPathIsDirectory(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	// Pre-create secrets.env as a DIRECTORY: os.Open succeeds but the
	// scanner read fails, exercising readFile's scan-error branch and
	// LoadOrGenerate's read-error wrap.
	if err := os.MkdirAll(filepath.Join(dir, secretsFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	err := LoadOrGenerate(context.Background(), dir, silentLogger())
	if err == nil {
		t.Fatal("LoadOrGenerate succeeded with secrets.env as a directory")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("err = %v, want read-step breadcrumb", err)
	}
}

func TestLoadOrGenerate_PersistFailureSurfaces(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	scrubEnv(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil { // r-x: CreateTemp must fail
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := LoadOrGenerate(context.Background(), dir, silentLogger())
	if err == nil {
		t.Fatal("LoadOrGenerate succeeded despite read-only data dir")
	}
	if !strings.Contains(err.Error(), "persist to") {
		t.Errorf("err = %v, want persist-step breadcrumb", err)
	}
}

func TestWriteFile_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := writeFile(ctx, filepath.Join(t.TempDir(), secretsFileName), map[string]string{"A": "b"})
	if err == nil {
		t.Fatal("writeFile with cancelled ctx succeeded")
	}
	if !strings.Contains(err.Error(), "ctx") {
		t.Errorf("err = %v, want ctx breadcrumb", err)
	}
}

func TestWriteFile_MkdirFailsWhenParentIsFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Parent of the target path is a regular file → MkdirAll must fail.
	err := writeFile(context.Background(), filepath.Join(blocker, "sub", secretsFileName),
		map[string]string{"A": "b"})
	if err == nil {
		t.Fatal("writeFile succeeded with a file as parent directory")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("err = %v, want mkdir breadcrumb", err)
	}
}

func TestWriteFile_CreateTempFailsInReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := writeFile(context.Background(), filepath.Join(dir, secretsFileName),
		map[string]string{"A": "b"})
	if err == nil {
		t.Fatal("writeFile succeeded in a read-only directory")
	}
	if !strings.Contains(err.Error(), "create temp") {
		t.Errorf("err = %v, want create-temp breadcrumb", err)
	}
}

func TestFsyncDir_MissingDirectory(t *testing.T) {
	err := fsyncDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("fsyncDir on a missing directory succeeded")
	}
	if !strings.Contains(err.Error(), "open dir") {
		t.Errorf("err = %v, want open-dir breadcrumb", err)
	}
}

func TestGenerateHex_LengthAndValidity(t *testing.T) {
	v, err := generateHex(32)
	if err != nil {
		t.Fatalf("generateHex: %v", err)
	}
	if len(v) != 64 {
		t.Errorf("len = %d, want 64 hex chars for 32 bytes", len(v))
	}
	if err := validateHex32(v); err != nil {
		t.Errorf("generated value fails the hex32 validator: %v", err)
	}
	// Two calls must not collide (sanity check on the entropy source).
	v2, err := generateHex(32)
	if err != nil {
		t.Fatalf("generateHex second call: %v", err)
	}
	if v == v2 {
		t.Error("two generateHex calls returned identical values")
	}
}

func TestWriteFile_RenameFailsWhenTargetIsDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, secretsFileName)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	err := writeFile(context.Background(), target, map[string]string{"A": "b"})
	if err == nil {
		t.Fatal("writeFile renamed over an existing directory")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("err = %v, want rename breadcrumb", err)
	}
}
