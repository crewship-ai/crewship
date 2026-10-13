package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootedMemoryReadPinsParentAcrossSwap(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "daily")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "entry.md")
	if err := os.WriteFile(path, []byte("canonical inside"), 0644); err != nil {
		t.Fatal(err)
	}
	file, err := openRootedMutationFile(base, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "entry.md"), []byte("outside secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := file.read()
	if err != nil || string(got) != "canonical inside" {
		t.Fatalf("pinned read=%q err=%v", got, err)
	}
	if _, err := ReadFileNoFollowRoot(base, path); err == nil {
		t.Fatal("new read followed replaced parent")
	}
}

func TestReindexPathRejectsReplacedParentWithoutIndexingOutside(t *testing.T) {
	base, engine := setupTestMemory(t)
	path := filepath.Join(base, "daily", "entry.md")
	if err := os.WriteFile(path, []byte("ordinary inside"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReindexPath(context.Background(), "daily/entry.md"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "entry.md"), []byte("OUTSIDE_PRIVATE_CREDENTIAL"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(base, "daily"), filepath.Join(base, "daily-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "daily")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	n, err := engine.ReindexPath(context.Background(), "daily/entry.md")
	if err != nil || n != 0 {
		t.Fatalf("unsafe index=%d err=%v", n, err)
	}
	var body string
	rows, err := engine.db.Query(`SELECT content FROM memory_chunks`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, "OUTSIDE_PRIVATE_CREDENTIAL") {
			t.Fatal("outside secret entered searchable index")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreWithStorageRootRejectsReplacedParent(t *testing.T) {
	db := openVersionsDB(t)
	root := t.TempDir()
	blobs := filepath.Join(root, "versions")
	v, err := RecordVersion(context.Background(), db, VersionRecord{WorkspaceID: "ws_test", Path: "AGENT.md", Tier: TierAgent, Content: []byte("historical"), BlobRoot: blobs})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "AGENT.md")
	if err := os.WriteFile(victim, []byte("outside untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "agent")
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Restore(context.Background(), db, filepath.Join(parent, "AGENT.md"), "ws_test", "AGENT.md", v.Sha256, "operator", blobs, TierAgent, root); err == nil {
		t.Fatal("restore followed outside parent")
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "outside untouched" {
		t.Fatalf("outside=%q %v", got, err)
	}
	if _, err := os.Lstat(victim + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("outside lock created: %v", err)
	}
}
