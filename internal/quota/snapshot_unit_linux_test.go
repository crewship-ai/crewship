//go:build linux

package quota

import (
	"errors"
	"io"
	"testing"
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
