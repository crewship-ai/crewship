//go:build linux

package restrictedruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeInputCatalogInstanceIsolationAndProtectedRecoveryRecord(t *testing.T) {
	source := func(context.Context, Plan) ([]NativeInputData, error) { return nil, ErrDenied }
	first, second := t.TempDir(), t.TempDir()
	if err := os.Chmod(first, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(second, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewFrozenNativeCatalog(first, Docker{}, source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFrozenNativeCatalog(second, Docker{}, source)
	if err != nil {
		t.Fatal(err)
	}
	if a.owner == b.owner {
		t.Fatal("cloned database IDs would alias host snapshot namespace")
	}
	suffix := nativeInputObjectSuffix("same-attempt")
	r := nativeSnapshotRecord{Attempt: "same-attempt", Volume: "crewship-rtest-input-" + a.owner + "-" + suffix, Populator: "crewship-rtest-input-writer-" + a.owner + "-" + suffix, State: "allocated"}
	if err := a.save(r); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFrozenNativeCatalog(a.dir, Docker{}, source)
	if err != nil || reopened.owner != a.owner {
		t.Fatal("restart lost immutable instance namespace", err)
	}
	got, err := reopened.load(r.Attempt)
	if err != nil || got != r {
		t.Fatal("durable cleanup record changed", err)
	}
	// A record copied from a different instance cannot authorize cleanup there.
	if err := b.save(r); err != nil {
		t.Fatal(err)
	}
	if _, err := b.load(r.Attempt); err == nil {
		t.Fatal("foreign instance record authorized snapshot operations")
	}
	name := filepath.Join(a.dir, "symlink-attempt.json")
	if err := os.Symlink(filepath.Join(a.dir, r.Attempt+".json"), name); err != nil {
		t.Fatal(err)
	}
	if _, err := a.load("symlink-attempt"); err == nil {
		t.Fatal("symlink journal accepted")
	}
}
