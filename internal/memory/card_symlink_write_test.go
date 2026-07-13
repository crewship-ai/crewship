package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// #1039: WritePeerCard / WriteUserModel used os.WriteFile, which follows a
// final-component symlink. These run in host-side consolidate routines
// (broader privilege than the agent) and slugs are attacker-predictable, so a
// crew agent can pre-plant peers/<slug>.md as a symlink to an out-of-tree host
// path; the routine then writes the card body through the link. The writers
// must refuse a symlinked target (and never write through it).

func TestWritePeerCard_RefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	p := PeerPaths{AgentDir: dir}

	// Sentinel outside the memory tree — the attacker's real target.
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "victim.txt")
	const original = "PRE-EXISTING HOST CONTENT"
	if err := os.WriteFile(sentinel, []byte(original), 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	// Plant the card path as a symlink to the sentinel.
	slug := UserSlug("u1", "ws1")
	cardPath := p.CardPath(slug)
	if err := os.MkdirAll(filepath.Dir(cardPath), 0o755); err != nil {
		t.Fatalf("mkdir peers: %v", err)
	}
	if err := os.Symlink(sentinel, cardPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	err := WritePeerCard(p, "u1", "ws1", "MALICIOUS CARD BODY")
	if err == nil {
		t.Errorf("WritePeerCard through a symlink should error")
	}
	got, rerr := os.ReadFile(sentinel)
	if rerr != nil {
		t.Fatalf("read sentinel: %v", rerr)
	}
	if string(got) != original {
		t.Fatalf("symlink target was written through: sentinel = %q, want %q", got, original)
	}
}

func TestWriteUserModel_RefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}

	outside := t.TempDir()
	sentinel := filepath.Join(outside, "victim.txt")
	const original = "PRE-EXISTING HOST CONTENT"
	if err := os.WriteFile(sentinel, []byte(original), 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	slug := UserSlug("u1", "ws1")
	modelPath := p.ModelPath(slug)
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatalf("mkdir users: %v", err)
	}
	if err := os.Symlink(sentinel, modelPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	err := WriteUserModel(p, "u1", "ws1", "MALICIOUS MODEL BODY")
	if err == nil {
		t.Errorf("WriteUserModel through a symlink should error")
	}
	got, rerr := os.ReadFile(sentinel)
	if rerr != nil {
		t.Fatalf("read sentinel: %v", rerr)
	}
	if string(got) != original {
		t.Fatalf("symlink target was written through: sentinel = %q, want %q", got, original)
	}
}

// The non-symlink happy path must still write durably (regression guard that
// the durable writer replaced os.WriteFile without changing behavior).
func TestWritePeerCard_RegularWriteStillWorks(t *testing.T) {
	dir := t.TempDir()
	p := PeerPaths{AgentDir: dir}
	if err := WritePeerCard(p, "u1", "ws1", "hello card"); err != nil {
		t.Fatalf("WritePeerCard: %v", err)
	}
	got, err := LoadPeerCard(p, "u1", "ws1")
	if err != nil || got != "hello card" {
		t.Fatalf("round-trip: got %q err %v", got, err)
	}
}
