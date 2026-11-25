package localfs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent plants a relative link, inside the storage tree, to another
// crew's directory. Reads and writes the server does for the first crew must
// not land in the second crew's files.
func TestCrewLinkInsideBaseDoesNotReachAnotherCrew(t *testing.T) {
	base := t.TempDir()
	p, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"crewA/slug", "crewB/slug"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(base, "crewB", "slug", "secret.txt")
	if err := os.WriteFile(secret, []byte("crew B only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../crewB/slug", filepath.Join(base, "crewA", "slug", "attachments")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Run("read", func(t *testing.T) {
		rc, err := p.Read(ctx, "crewA/slug/attachments/secret.txt")
		if err == nil {
			b, _ := io.ReadAll(rc)
			rc.Close()
			if strings.Contains(string(b), "crew B") {
				t.Fatal("a read for crew A returned crew B's file")
			}
		}
	})
	t.Run("write", func(t *testing.T) {
		_ = p.Write(ctx, "crewA/slug/attachments/new.txt", strings.NewReader("from crew A"))
		if _, err := os.Stat(filepath.Join(base, "crewB", "slug", "new.txt")); err == nil {
			t.Fatal("a write for crew A landed in crew B's directory")
		}
	})
}

// A link that stays inside the crew's own directory keeps working, and so
// does a link to a server-owned area being refused: a link may not change
// which crew, workspace or area a path belongs to.
func TestLinkInsideOwnCrewDirectoryStillWorks(t *testing.T) {
	base := t.TempDir()
	p, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"crewA/slug/real", "attachments/ws1"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "crewA", "slug", "real", "a.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "attachments", "ws1", "blob"), []byte("server blob"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(base, "crewA", "slug", "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../attachments/ws1", filepath.Join(base, "crewA", "slug", "server")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rc, err := p.Read(ctx, "crewA/slug/alias/a.txt")
	if err != nil {
		t.Fatalf("link inside the crew directory refused: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "mine" {
		t.Fatalf("read %q", b)
	}
	if err := p.Write(ctx, "crewA/slug/alias/new.txt", strings.NewReader("ok")); err != nil {
		t.Fatalf("write through an in-crew link refused: %v", err)
	}
	if rc, err := p.Read(ctx, "crewA/slug/server/blob"); err == nil {
		rc.Close()
		t.Fatal("a crew link reached the server's attachment store")
	}
	if err := p.Write(ctx, "crewA/slug/server/extra", strings.NewReader("x")); err == nil {
		t.Fatal("a crew link let a write into the server's attachment store")
	}
	if _, err := os.Stat(filepath.Join(base, "attachments", "ws1", "extra")); err == nil {
		t.Fatal("file landed in the attachment store")
	}
}

// A link whose target does not exist yet (dangling) is followed by a create.
// It must not carry a new file or directory into another crew's directory or
// the server's attachment store, directly or through a chain of two links.
func TestDanglingLinkDoesNotCreateInAnotherUnit(t *testing.T) {
	base := t.TempDir()
	p, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"crewA/slug", "crewB/slug", "attachments/ws1"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		"crewA/slug/to-b":      "../../crewB/slug/new",
		"crewA/slug/to-b-dir":  "../../crewB/slug/newdir",
		"crewA/slug/to-attach": "../../attachments/ws1/new",
		"crewA/slug/chain-1":   "chain-2",
		"crewA/slug/chain-2":   "../../crewB/slug/chained",
		"crewA/slug/to-b-abs":  filepath.Join(base, "crewB", "slug", "absnew"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for _, name := range []string{"to-b", "to-attach", "chain-1", "to-b-abs"} {
		_ = p.Write(ctx, "crewA/slug/"+name, strings.NewReader("from crew A"))
	}
	_ = p.Write(ctx, "crewA/slug/to-b-dir/file.txt", strings.NewReader("from crew A"))
	_ = p.EnsureDir(ctx, "crewA/slug/to-b-dir/sub")
	for _, gone := range []string{"crewB/slug/new", "crewB/slug/newdir", "crewB/slug/chained", "crewB/slug/absnew", "attachments/ws1/new"} {
		if _, err := os.Lstat(filepath.Join(base, gone)); err == nil {
			t.Errorf("%s was created through a dangling link", gone)
		}
	}
}

// Listing and existence checks on a unit that is not there are not errors.
func TestMissingUnitReadsAreEmpty(t *testing.T) {
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if files, err := p.List(ctx, "nocrew/slug"); err != nil || len(files) != 0 {
		t.Errorf("List: %v %v", files, err)
	}
	if files, err := p.ListRecursive(ctx, "nocrew/slug"); err != nil || len(files) != 0 {
		t.Errorf("ListRecursive: %v %v", files, err)
	}
	if ok, err := p.Exists(ctx, "nocrew/slug/f"); err != nil || ok {
		t.Errorf("Exists: %v %v", ok, err)
	}
	if err := p.Delete(ctx, "nocrew/slug/f"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := p.Write(ctx, "newcrew/slug/f.txt", strings.NewReader("x")); err != nil {
		t.Errorf("Write into a new crew: %v", err)
	}
}

// An administrator may put the whole storage base on another disk and link it
// in: read, write, list, delete and upload keep working. (A link at a crew's
// own directory has never worked here: the base-wide containment check and
// os.Root refuse it, on main as well.)
func TestAdministratorLinkedBaseWorks(t *testing.T) {
	ctx := context.Background()
	exercise := func(t *testing.T, p *Provider, crewPath string) {
		t.Helper()
		if err := p.Write(ctx, crewPath+"/slug/attachments/chat1/att1/file.txt", strings.NewReader("hello")); err != nil {
			t.Fatalf("write: %v", err)
		}
		rc, err := p.Read(ctx, crewPath+"/slug/attachments/chat1/att1/file.txt")
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != "hello" {
			t.Fatalf("read %q", b)
		}
		files, err := p.ListRecursive(ctx, crewPath+"/slug")
		if err != nil || len(files) == 0 {
			t.Fatalf("list: %v %v", files, err)
		}
		if err := p.Delete(ctx, crewPath+"/slug/attachments/chat1"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if ok, _ := p.Exists(ctx, crewPath+"/slug/attachments/chat1"); ok {
			t.Fatal("still there after delete")
		}
	}
	t.Run("base is a link to another disk", func(t *testing.T) {
		disk := t.TempDir()
		link := filepath.Join(t.TempDir(), "storage")
		if err := os.Symlink(disk, link); err != nil {
			t.Fatal(err)
		}
		p, err := New(link)
		if err != nil {
			t.Fatal(err)
		}
		exercise(t, p, "crewA")
	})
}
