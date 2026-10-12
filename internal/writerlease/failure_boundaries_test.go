package writerlease

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLeaseRejectsInvalidAnchorsAndLosesMissingHandles(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{filepath.Join(dir, "missing", "state.db"), dir} {
		lease, err := Acquire(path)
		if lease != nil {
			lease.Close()
			t.Fatal("invalid anchor acquired")
		}
		if err == nil {
			t.Fatalf("invalid path %s accepted", path)
		}
	}
	var absent *Lease
	if !errors.Is(absent.Verify(), ErrLost) {
		t.Fatal("nil lease verified")
	}
	if err := absent.Close(); err != nil {
		t.Fatalf("nil close: %v", err)
	}
	path := filepath.Join(dir, "state.db")
	lease, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := lease.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Verify(); !errors.Is(err, ErrLost) || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed handle lost cause: %v", err)
	}
	if err := lease.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close should report closed descriptor: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("repeat close=%v", err)
	}
}
