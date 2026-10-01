//go:build linux

package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const unitSize = int64(64 << 20)

// One bad catalog entry must not stop the helper (and, through the server
// unit's dependency, the whole server). Recover moves bad entries aside,
// reports them, and still mounts every good one.
func TestRecoverQuarantinesBadEntries(t *testing.T) {
	good := Key{"crew", "database", "data", 1}
	cases := []struct {
		name            string
		plant           func(t *testing.T, u *unitBackend, k Key)
		wantQuarantined []string // file names expected under quarantine/
		wantCleaned     bool
		wantGone        []string // file names expected absent from images/
	}{
		{
			name:            "orphaned metadata without image",
			plant:           func(t *testing.T, u *unitBackend, k Key) { u.writeEntry(t, k, unitSize, false, true) },
			wantQuarantined: []string{".json"},
		},
		{
			name:            "image without metadata",
			plant:           func(t *testing.T, u *unitBackend, k Key) { u.writeEntry(t, k, unitSize, true, false) },
			wantQuarantined: []string{".ext4"},
		},
		{
			name: "corrupt metadata",
			plant: func(t *testing.T, u *unitBackend, k Key) {
				u.writeEntry(t, k, unitSize, true, false)
				if err := os.WriteFile(filepath.Join(u.root, "images", k.id()+".json"), []byte("{not json"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantQuarantined: []string{".json", ".ext4"},
		},
		{
			name: "image size disagrees with metadata",
			plant: func(t *testing.T, u *unitBackend, k Key) {
				u.writeEntry(t, k, unitSize, false, true)
				u.writeEntry(t, k, unitSize*2, true, false)
			},
			wantQuarantined: []string{".json", ".ext4"},
		},
		{
			name: "crash during allocation",
			plant: func(t *testing.T, u *unitBackend, k Key) {
				u.writeEntry(t, k, unitSize, true, false)
				u.writeAllocating(t, k, unitSize)
			},
			wantCleaned: true,
			wantGone:    []string{".ext4", ".allocating"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnitBackend(t)
			bad := Key{"crew", "database", "broken", 1}
			u.writeEntry(t, good, unitSize, true, true)
			tc.plant(t, u, bad)
			report, err := u.RecoverReport(t.Context())
			if err != nil {
				t.Fatalf("bad entry aborted recovery: %v", err)
			}
			if len(u.attached) != 1 || u.attached[0].Key != good || !slices.Contains(report.Mounted, good.id()) {
				t.Fatalf("good entry not recovered: attached %v report %+v", u.attached, report)
			}
			for _, suffix := range tc.wantQuarantined {
				if u.exists(filepath.Join("images", bad.id()+suffix)) {
					t.Fatalf("%s left in the live catalog", suffix)
				}
				if !u.quarantined(t, bad.id()+suffix) {
					t.Fatalf("%s not moved to quarantine", suffix)
				}
			}
			if len(tc.wantQuarantined) > 0 && !slices.Contains(report.Quarantined, bad.id()) {
				t.Fatalf("quarantine not reported: %+v", report)
			}
			if tc.wantCleaned && !slices.Contains(report.Cleaned, bad.id()) {
				t.Fatalf("crashed allocation not reported as cleaned: %+v", report)
			}
			for _, suffix := range tc.wantGone {
				if u.exists(filepath.Join("images", bad.id()+suffix)) {
					t.Fatalf("crashed allocation left %s behind", suffix)
				}
			}
			if report.Summary() == "" {
				t.Fatal("empty recovery summary")
			}
		})
	}
}

func TestRecoverReportsAttachFailureWithoutAborting(t *testing.T) {
	u := newUnitBackend(t)
	failing := Key{"crew", "database", "data", 1}
	healthy := Key{"crew", "cache", "data", 1}
	u.writeEntry(t, failing, unitSize, true, true)
	u.writeEntry(t, healthy, unitSize, true, true)
	u.attach = func(_ context.Context, d Descriptor) error {
		if d.Key == failing {
			return errors.New("loop device busy")
		}
		u.attached = append(u.attached, d)
		return nil
	}
	report, err := u.RecoverReport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(report.Failed, failing.id()) || !slices.Contains(report.Mounted, healthy.id()) {
		t.Fatalf("attach failure not isolated: %+v", report)
	}
	if !u.exists(filepath.Join("images", failing.id()+".json")) {
		t.Fatal("a transient attach failure must not quarantine a valid entry")
	}
}

// The allocating record is written before the image exists, so a crash at
// any point of allocation leaves evidence the key was never handed out.
func TestEnsureWritesAllocatingRecordFirst(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	recordDuringMkfs := false
	u.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if filepath.Base(name) == "mkfs.ext4" {
			recordDuringMkfs = u.exists(filepath.Join("images", k.id()+".allocating"))
		}
		return nil, nil
	}
	if _, err := u.Ensure(t.Context(), k, unitSize, Owner{}); err != nil {
		t.Fatal(err)
	}
	if !recordDuringMkfs {
		t.Fatal("image formatted without an allocating record")
	}
	if u.exists(filepath.Join("images", k.id()+".allocating")) || !u.exists(filepath.Join("images", k.id()+".json")) {
		t.Fatal("completed allocation left its record or lost its metadata")
	}
}

func TestEnsureAndRemoveAdoptCrashedAllocation(t *testing.T) {
	for _, op := range []string{"ensure", "remove"} {
		t.Run(op, func(t *testing.T) {
			u := newUnitBackend(t)
			k := Key{"crew", "database", "data", 1}
			u.writeEntry(t, k, unitSize, true, false)
			u.writeAllocating(t, k, unitSize)
			var err error
			if op == "ensure" {
				_, err = u.Ensure(t.Context(), k, unitSize, Owner{})
			} else {
				err = u.Remove(t.Context(), k)
			}
			if err != nil {
				t.Fatalf("crashed allocation wedged the key: %v", err)
			}
			if op == "ensure" && len(u.ran("mkfs.ext4")) != 1 {
				t.Fatal("crashed image reused instead of reallocated")
			}
			if op == "remove" && (u.exists(filepath.Join("images", k.id()+".ext4")) || u.exists(filepath.Join("images", k.id()+".allocating"))) {
				t.Fatal("crashed allocation not cleaned")
			}
		})
	}
}

// Quarantined images still occupy disk; aggregate admission keeps counting
// them until an administrator deletes them.
func TestQuarantinedImagesStillCountTowardCapacity(t *testing.T) {
	u := newUnitBackend(t)
	u.capacity = unitSize
	orphan := Key{"crew", "database", "orphan", 1}
	u.writeEntry(t, orphan, unitSize, true, false)
	if _, err := u.RecoverReport(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Ensure(t.Context(), Key{"crew", "database", "data", 1}, unitSize, Owner{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("quarantined image ignored by capacity admission: %v", err)
	}
}
