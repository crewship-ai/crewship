//go:build unix

package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMutateRootedPinsParentDuringAuthorizedWrite(t *testing.T) {
	f := newMutateFixture(t)
	parent := filepath.Join(f.dir, "agent")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	f.path = filepath.Join(parent, "AGENT.md")
	req := f.req("rooted-seed", OpReplace, "inside\n")
	req.StorageRoot = f.dir
	if _, err := MutateRooted(context.Background(), f.db, req); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "AGENT.md")
	if err := os.WriteFile(victim, []byte("outside secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	req = f.req("rooted-next", OpAppend, "next\n")
	req.StorageRoot = f.dir
	req.Authorize = func(context.Context) error {
		if err := os.Rename(parent, parent+"-original"); err != nil {
			return err
		}
		return os.Symlink(outside, parent)
	}
	if _, err := MutateRooted(context.Background(), f.db, req); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(parent+"-original", "AGENT.md"))
	if err != nil || string(got) != "inside\nnext\n" {
		t.Fatalf("pinned=%q %v", got, err)
	}
	got, err = os.ReadFile(victim)
	if err != nil || string(got) != "outside secret\n" {
		t.Fatalf("outside=%q %v", got, err)
	}
	if _, err := ReadCanonicalRooted(context.Background(), nil, ReadRequest{Path: f.path, StorageRoot: f.dir, AuditPath: "agent/AGENT.md"}); err == nil {
		t.Fatal("rooted read followed replaced parent")
	}
}
