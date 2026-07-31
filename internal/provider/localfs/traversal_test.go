package localfs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The escapes pinned here all share one shape: a symlink planted at an
// *intermediate* component of the path, with a leaf that does not exist yet.
//
// That combination used to slip past the guard. resolve() only re-checked
// containment when filepath.EvalSymlinks succeeded, and EvalSymlinks fails on
// a path whose last component is missing — so for exactly the operations that
// create something new (Write, EnsureDir) the unresolved path was handed
// straight to os.Create / os.MkdirAll, which follow symlinks. An agent
// container writing into the shared bind-mount at its own uid can plant that
// symlink, so this was reachable, not theoretical.
//
// Every operation now goes through an *os.Root anchored at basePath, which
// validates each component as it walks and refuses any symlink leaving the
// root — see openRoot.

// escapeFixture returns a provider whose base contains "link" → an outside
// directory, plus the path of that outside directory.
func escapeFixture(t *testing.T) (*Provider, string) {
	t.Helper()
	outside := t.TempDir()
	p := tempProvider(t)
	if err := os.Symlink(outside, filepath.Join(p.basePath, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return p, outside
}

// Write through a symlinked intermediate component is arbitrary file write.
func TestWrite_IntermediateSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	p, outside := escapeFixture(t)

	err := p.Write(context.Background(), "link/pwned.txt", bytes.NewReader([]byte("owned")))
	if err == nil {
		t.Fatal("Write through a symlinked parent must be refused")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "pwned.txt")); statErr == nil {
		t.Fatalf("Write escaped the base: %s was created", filepath.Join(outside, "pwned.txt"))
	}
}

// Same escape one level deeper: the symlink is not the first component.
func TestWrite_NestedIntermediateSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.EnsureDir(ctx, "agent-a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.basePath, "agent-a", "out")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := p.Write(ctx, "agent-a/out/sub/pwned.txt", bytes.NewReader([]byte("owned"))); err == nil {
		t.Fatal("Write through a nested symlinked parent must be refused")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "sub")); statErr == nil {
		t.Fatalf("Write escaped the base: %s was created", filepath.Join(outside, "sub"))
	}
}

// EnsureDir through a symlinked intermediate component creates directories
// outside the base.
func TestEnsureDir_IntermediateSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	p, outside := escapeFixture(t)

	if err := p.EnsureDir(context.Background(), "link/pwned"); err == nil {
		t.Fatal("EnsureDir through a symlinked parent must be refused")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "pwned")); statErr == nil {
		t.Fatalf("EnsureDir escaped the base: %s was created", filepath.Join(outside, "pwned"))
	}
}

// Delete through a symlinked intermediate component must not reach outside,
// and must leave the escaping target intact.
func TestDelete_IntermediateSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	p, outside := escapeFixture(t)
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := p.Delete(context.Background(), "link/victim.txt"); err == nil {
		t.Error("Delete through a symlinked parent must be refused")
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Fatalf("Delete escaped the base and removed %s", victim)
	}
}

// Read through a symlinked intermediate component must not serve outside
// content.
func TestRead_IntermediateSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	p, outside := escapeFixture(t)
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("topsecret"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := p.Read(context.Background(), "link/secret.txt")
	if err == nil {
		r.Close()
		t.Fatal("Read through a symlinked parent must be refused")
	}
}

// List through a symlinked intermediate component must not enumerate outside.
func TestList_IntermediateSymlinkEscapeRefused(t *testing.T) {
	t.Parallel()
	p, outside := escapeFixture(t)
	if err := os.MkdirAll(filepath.Join(outside, "secrets"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secrets", "k.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := p.List(context.Background(), "link/secrets")
	if err == nil && len(files) > 0 {
		t.Fatalf("List escaped the base and enumerated %v", files)
	}
}

// Deleting the storage root itself is refused. The old code resolved "" and
// "." to basePath and handed it to os.RemoveAll, wiping every crew's output
// on a caller bug.
func TestDelete_RefusesStorageRoot(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.Write(ctx, "keep.txt", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"", ".", "./"} {
		err := p.Delete(ctx, path)
		if err == nil {
			t.Errorf("Delete(%q) must be refused", path)
		} else if !strings.Contains(err.Error(), "storage root") {
			t.Errorf("Delete(%q) error = %q, want a storage-root refusal", path, err)
		}
	}
	if _, statErr := os.Stat(p.basePath); statErr != nil {
		t.Fatalf("storage root was removed: %v", statErr)
	}
	if ok, _ := p.Exists(ctx, "keep.txt"); !ok {
		t.Error("existing file was removed with the storage root")
	}
}

// A relative in-base symlink keeps resolving — the hardening must not break
// the legitimate case.
func TestWrite_RelativeInBaseSymlinkDirStillWorks(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx := context.Background()
	if err := p.EnsureDir(ctx, "real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(p.basePath, "alias")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := p.Write(ctx, "alias/f.txt", bytes.NewReader([]byte("ok"))); err != nil {
		t.Fatalf("in-base symlinked dir should still be writable: %v", err)
	}
	if ok, _ := p.Exists(ctx, "real/f.txt"); !ok {
		t.Error("write through the in-base symlink did not land on the real target")
	}
}
