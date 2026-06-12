package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFile_NilContext_DefaultsToBackground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENT.md")
	//nolint:staticcheck // passing nil ctx on purpose — the contract under test
	res, err := WriteFile(nil, path, []byte("body"), WriteConfig{})
	if err != nil {
		t.Fatalf("WriteFile(nil ctx): %v", err)
	}
	if res.Rejected || res.BytesWritten != 4 {
		t.Errorf("result = %+v, want clean 4-byte write", res)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "body" {
		t.Errorf("on-disk = %q (%v), want body", data, readErr)
	}
}

func TestWriteFile_MkdirParentFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WriteFile(context.Background(), filepath.Join(blocker, "sub", "AGENT.md"), []byte("b"), WriteConfig{})
	if err == nil || !strings.Contains(err.Error(), "mkdir parent") {
		t.Fatalf("err = %v, want mkdir-parent error", err)
	}
}

func TestWriteFile_RenameFailure_TargetIsDirectory_NoTempLeak(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "AGENT.md")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := WriteFile(context.Background(), target, []byte("b"), WriteConfig{})
	if err == nil || !strings.Contains(err.Error(), "atomic rename") {
		t.Fatalf("err = %v, want atomic-rename error", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("tempfile leaked after failed rename: %s", e.Name())
		}
	}
}

func TestWriteFile_TempfileOpenFailure_ReadOnlyDirWithPreexistingLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")
	// Pre-create the lock sentinel so Lock() needs no directory write
	// access; the tempfile O_CREATE is then the first write the
	// read-only directory rejects.
	if err := os.WriteFile(path+".lock", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := WriteFile(context.Background(), path, []byte("b"), WriteConfig{})
	if err == nil || !strings.Contains(err.Error(), "open tempfile") {
		t.Fatalf("err = %v, want open-tempfile error", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("target must not exist after tempfile failure")
	}
}

func TestWriteFile_ParentDirFsyncOpenFailure_WriteOnlyDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")
	// Write+search but NO read permission: every write syscall in the
	// sequence works, but the final os.Open(dir) for the fsync fails.
	if err := os.Chmod(dir, 0o333); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := WriteFile(context.Background(), path, []byte("b"), WriteConfig{})
	if err == nil || !strings.Contains(err.Error(), "open parent dir for fsync") {
		t.Fatalf("err = %v, want parent-dir-fsync open error", err)
	}
	// The data file itself IS on disk — the dir-entry fsync is what
	// failed, and WriteFile documents it does not roll back.
	_ = os.Chmod(dir, 0o755)
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "b" {
		t.Errorf("renamed file = %q (%v), want b on disk despite fsync-open failure", data, readErr)
	}
}

func TestWriteFile_CancelledWhileWaitingForLock_NoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")

	lk := NewFileLock(path + ".lock")
	if err := lk.Lock(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := WriteFile(ctx, path, []byte("never lands"), WriteConfig{})
		done <- err
	}()

	time.Sleep(60 * time.Millisecond) // let the writer block on the flock
	cancel()
	if err := lk.Unlock(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		// Either the preflight or the post-lock context check fired —
		// both reject with a context error and neither touches disk.
		if err == nil || !strings.Contains(err.Error(), "context check") {
			t.Fatalf("err = %v, want context-check rejection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteFile did not return")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file must not exist after cancelled write")
	}
}
