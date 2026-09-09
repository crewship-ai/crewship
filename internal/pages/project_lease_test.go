package pages

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestProjectLeaseProtectsReadersAndWorkspaceIsolation(t *testing.T) {
	store := &ProjectStore{Directory: t.TempDir()}
	ctx := context.Background()
	read, err := store.Lease(ctx, "workspace", false)
	if err != nil {
		t.Fatal(err)
	}
	defer read()
	another := &ProjectStore{Directory: store.Directory}
	shared, err := another.Lease(ctx, "workspace", false)
	if err != nil {
		t.Fatal(err)
	}
	shared()
	deadline, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	if release, err := another.Lease(deadline, "workspace", true); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("maintenance bypassed reader: %v", err)
	}
	foreign, err := another.Lease(ctx, "other-workspace", true)
	if err != nil {
		t.Fatal(err)
	}
	foreign()
	read()
	read() // release is idempotent
	exclusive, err := another.Lease(ctx, "workspace", true)
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive()
	deadline2, cancel2 := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel2()
	if release, err := store.Lease(deadline2, "workspace", false); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("reader bypassed maintenance: %v", err)
	}
}

func TestGitRestoreQuotaRefusesExistingFullNamespace(t *testing.T) {
	ctx := context.Background()
	target := &ProjectStore{Directory: t.TempDir()}
	stage := &ProjectStore{Directory: t.TempDir()}
	// Owned sparse fixture exercises byte accounting without allocating 128 MiB.
	root, err := target.root("ws", true)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.MkdirAll("git/fixture", 0700); err != nil {
		t.Fatal(err)
	}
	f, err := root.OpenFile("git/fixture/full", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxProjectGitBytes); err != nil {
		t.Fatal(err)
	}
	f.Close()
	staged, err := stage.root("ws", true)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if err := staged.MkdirAll("git/fixture", 0700); err != nil {
		t.Fatal(err)
	}
	f, err = staged.OpenFile("git/fixture/new", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("new"))
	f.Close()
	if err := target.CheckGitRestoreQuota(ctx, "ws", stage); !errors.Is(err, ErrProjectGitFull) {
		t.Fatalf("restore bypassed Git quota: %v", err)
	}
	if _, err := root.Stat("git/fixture/new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quota preflight wrote staged file")
	}
}

func TestGitMaintenanceDoesNotBlockArtifactReadersButExcludesCheckpointWrites(t *testing.T) {
	ctx := context.Background()
	store := &ProjectStore{Directory: t.TempDir()}
	workspace, err := store.Lease(ctx, "ws", false)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace()
	git, err := store.GitLease(ctx, "ws", true)
	if err != nil {
		t.Fatal(err)
	}
	defer git()
	deadline, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
	defer cancel()
	reader, err := store.Lease(deadline, "ws", false)
	if err != nil {
		t.Fatal("Git maintenance blocked artifact reader", err)
	}
	reader()
	if writer, err := store.GitLease(deadline, "ws", false); !errors.Is(err, context.DeadlineExceeded) {
		if writer != nil {
			writer()
		}
		t.Fatalf("object writer overlapped GC: %v", err)
	}
}
