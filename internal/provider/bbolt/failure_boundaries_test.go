package bbolt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateStorageFailuresReturnNoPartialValues(t *testing.T) {
	p, err := New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Set(t.Context(), "", "key", []byte("value")); err == nil {
		t.Fatal("empty bucket accepted")
	}
	if values, err := p.List(t.Context(), "missing"); err != nil || len(values) != 0 {
		t.Fatalf("missing bucket: %v, %v", values, err)
	}
	if values, err := p.ListByPrefix(t.Context(), "missing", "key"); err != nil || len(values) != 0 {
		t.Fatalf("missing prefix bucket: %v, %v", values, err)
	}
	if err := p.Set(t.Context(), "bucket", "key", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if value, err := p.Get(t.Context(), "bucket", "key"); err == nil || value != nil {
		t.Fatalf("closed get: %v, %v", value, err)
	}
	if values, err := p.List(t.Context(), "bucket"); err == nil || values != nil {
		t.Fatalf("closed list: %v, %v", values, err)
	}
	if values, err := p.ListByPrefix(t.Context(), "bucket", "key"); err == nil || values != nil {
		t.Fatalf("closed prefix list: %v, %v", values, err)
	}
	if err := p.Set(t.Context(), "bucket", "key", []byte("new")); err == nil {
		t.Fatal("closed write accepted")
	}
	if err := p.Delete(t.Context(), "bucket", "key"); err == nil {
		t.Fatal("closed delete accepted")
	}
}

func TestStateDatabaseRejectsUnavailablePaths(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(parent, "state.db"), t.TempDir()} {
		if p, err := New(path); err == nil || p != nil {
			t.Fatalf("opened unavailable path: %v, %v", p, err)
		}
	}
	raw, err := os.ReadFile(parent)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("existing file changed: %q, %v", raw, err)
	}
	db, err := openLocked(filepath.Join(t.TempDir(), "bounded.db"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
