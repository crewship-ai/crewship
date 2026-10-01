//go:build linux

package quota

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// unitBackend is a Backend over a private temp catalog owned by the test
// UID, with system tools and loop mounts replaced by recorders. It exercises
// the catalog bookkeeping without root; the physical path is covered by the
// quota_live / quota_vm suites.
type unitBackend struct {
	*Backend
	mu       sync.Mutex
	commands [][]string
	attached []Descriptor
}

func newUnitBackend(t *testing.T) *unitBackend {
	t.Helper()
	root := filepath.Join(t.TempDir(), "catalog")
	for _, dir := range []string{root, filepath.Join(root, "images"), filepath.Join(root, "mounts")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := os.OpenFile(filepath.Join(root, "catalog.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	u := &unitBackend{}
	u.Backend = &Backend{
		root: root, capacity: 1 << 30, lock: lock,
		owner:       uint32(os.Geteuid()),
		reservePath: filepath.Join(root, "reserve.lock"),
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.commands = append(u.commands, append([]string{name}, args...))
			return nil, nil
		},
		attach: func(_ context.Context, d Descriptor) error {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.attached = append(u.attached, d)
			return nil
		},
	}
	return u
}

// Teardown retries after a partial failure: once the image is gone, Release
// and Remove must succeed rather than wedge the Docker-volume cleanup.
func TestReleaseAndRemoveAreIdempotentForRemovedImage(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	if err := u.Release(t.Context(), k, "crewship-quota-x"); err != nil {
		t.Fatalf("release of a removed image: %v", err)
	}
	if err := u.Remove(t.Context(), k); err != nil {
		t.Fatalf("remove of a removed image: %v", err)
	}
}
