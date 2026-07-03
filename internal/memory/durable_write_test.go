package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileDurable_PersistsAndAtomic is the 2a durability guarantee.
// A successful durable write leaves exactly the new bytes and no tempfile
// residue.
func TestWriteFileDurable_PersistsAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")
	if err := os.WriteFile(path, []byte("OLD"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := writeFileDurable(path, []byte("NEW-DURABLE"), 0o644); err != nil {
		t.Fatalf("writeFileDurable: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "NEW-DURABLE" {
		t.Fatalf("content = %q, want NEW-DURABLE", got)
	}

	// No sibling tempfile left behind (the atomic rename consumed it).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != "" && len(e.Name()) > len("AGENT.md") &&
			e.Name() != "AGENT.md" {
			if hasTmpMarker(e.Name()) {
				t.Fatalf("tempfile residue left behind: %s", e.Name())
			}
		}
	}
}

// TestWriteFileDurable_AllOrNothingOnFailure is the property os.WriteFile
// lacks: when the write cannot complete, the ORIGINAL file survives
// intact rather than being truncated in place. We force failure by making
// the target directory read-only so the tempfile can't be created.
func TestWriteFileDurable_AllOrNothingOnFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; skip the RO-dir failure injection")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENT.md")
	if err := os.WriteFile(path, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := writeFileDurable(path, []byte("REPLACEMENT"), 0o644); err == nil {
		t.Fatal("expected error when the durable write cannot complete")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "ORIGINAL" {
		t.Fatalf("original must be preserved on failure; got %q", got)
	}
}

func hasTmpMarker(name string) bool {
	for i := 0; i+4 < len(name); i++ {
		if name[i:i+5] == ".tmp." {
			return true
		}
	}
	return false
}
