//go:build linux

package quota

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// writeEntry plants a catalog entry as a previous helper process left it.
func (u *unitBackend) writeEntry(t *testing.T, k Key, size int64, withImage, withMeta bool) {
	t.Helper()
	meta, image, mount := u.paths(k)
	if withImage {
		f, err := os.OpenFile(image, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if withMeta {
		raw, _ := json.Marshal(Descriptor{ID: k.id(), Key: k, Bytes: size, Mount: mount})
		if err := os.WriteFile(meta, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func (u *unitBackend) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(u.root, name))
	return err == nil
}

func (u *unitBackend) ran(tool string) [][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out [][]string
	for _, c := range u.commands {
		if strings.HasSuffix(c[0], tool) {
			out = append(out, c)
		}
	}
	return out
}

// writeAllocating plants the record an interrupted allocation leaves.
func (u *unitBackend) writeAllocating(t *testing.T, k Key, size int64) {
	t.Helper()
	raw, _ := json.Marshal(Descriptor{ID: k.id(), Key: k, Bytes: size})
	if err := os.WriteFile(filepath.Join(u.root, "images", k.id()+".allocating"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// quarantined reports whether a file named prefix* sits in quarantine/.
func (u *unitBackend) quarantined(t *testing.T, prefix string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(u.root, "quarantine"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			return true
		}
	}
	return false
}

func TestFailedAllocationMarkerClosesAndRemovesImage(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	u.writeMarker = func(string, []byte) error { return errors.New("marker write failed") }
	if _, err := u.Ensure(t.Context(), k, 64<<20, Owner{}); err == nil {
		t.Fatal("marker failure accepted")
	}
	_, image, _ := u.paths(k)
	for _, path := range []string{image, u.allocatingPath(k), u.unpreparedPath(k)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("failed allocation left %s: %v", filepath.Base(path), err)
		}
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		target, _ := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if target == image || target == image+" (deleted)" {
			t.Error("failed allocation leaked the image fd")
		}
	}
}
