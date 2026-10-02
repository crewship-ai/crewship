//go:build linux

package quota

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type snapshotUnitZeros struct{}

func (snapshotUnitZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestSnapshotImportCountsQuarantinedImages(t *testing.T) {
	u := newUnitBackend(t)
	u.capacity = MinBytes
	orphan := Key{"crew", "database", "orphan", 1}
	u.writeEntry(t, orphan, MinBytes, true, false)
	if _, err := u.RecoverReport(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Import(t.Context(), Key{"crew", "database", "data", 1}, MinBytes, io.LimitReader(snapshotUnitZeros{}, MinBytes)); !errors.Is(err, ErrDenied) {
		t.Fatalf("quarantined bytes omitted from import capacity: %v", err)
	}
}

type stalledSnapshotReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *stalledSnapshotReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, io.EOF
}
func TestSnapshotImportDoesNotBlockOtherKeys(t *testing.T) {
	u := newUnitBackend(t)
	key := Key{"crew", "database", "data", 1}
	reader := &stalledSnapshotReader{started: make(chan struct{}), release: make(chan struct{})}
	imported := make(chan error, 1)
	go func() { _, err := u.Import(t.Context(), key, MinBytes, reader); imported <- err }()
	defer func() { close(reader.release); <-imported }()
	select {
	case <-reader.started:
	case <-time.After(3 * time.Second):
		t.Fatal("import did not start")
	}
	released := make(chan error, 1)
	go func() {
		released <- u.Release(t.Context(), Key{"other", "database", "data", 1}, "crewship-quota-other")
	}()
	select {
	case err := <-released:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("streaming image blocks unrelated catalog control")
	}
	if _, err := u.Ensure(t.Context(), key, MinBytes, Owner{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("active image admitted a second operator: %v", err)
	}
	if _, err := u.RecoverReport(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("recovery can sweep active import: %v", err)
	}
}
