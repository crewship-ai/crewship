package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// recorder counts Record calls and sees what the saving transaction holds.
type recorder struct {
	calls    []int64
	inTxRows []int // artifact_versions rows of the version, read on the saving transaction
	fail     error
}

func (r *recorder) record(ctx context.Context, tx *sql.Tx, v VersionReceipt) error {
	r.calls = append(r.calls, v.Version)
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_versions WHERE workspace_id='ws' AND page_id='page' AND version=?`, v.Version).Scan(&n); err != nil {
		return err
	}
	r.inTxRows = append(r.inTxRows, n)
	return r.fail
}

func versionCount(t *testing.T, s *VersionStore) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE workspace_id='ws' AND page_id='page'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Record runs once per new version, on the transaction that inserted it, with its number.
func TestVersionRecordRunsOnTheSavingTransaction(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	rec := &recorder{}
	r := versionRequest(0, "first")
	r.Record = rec.record
	got, err := s.Apply(t.Context(), k, r, a)
	if err != nil || got.Version != 1 {
		t.Fatalf("apply: %+v %v", got, err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != 1 || rec.inTxRows[0] != 1 {
		t.Fatalf("record calls %v, rows seen %v", rec.calls, rec.inTxRows)
	}
	r = versionRequest(1, "second")
	r.Record = rec.record
	if got, err = s.Apply(t.Context(), k, r, a); err != nil || got.Version != 2 || len(rec.calls) != 2 || rec.calls[1] != 2 {
		t.Fatalf("second: %+v %v %v", got, err, rec.calls)
	}
}

// A failed Record rolls the version back: both or nothing.
func TestVersionRecordFailureRollsTheVersionBack(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "first"), a); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("audit refused")
	rec := &recorder{fail: boom}
	r := versionRequest(1, "second")
	r.Record = rec.record
	if _, err := s.Apply(t.Context(), k, r, a); !errors.Is(err, boom) {
		t.Fatalf("apply: %v", err)
	}
	if _, _, err := s.ApplyChanged(t.Context(), k, r, a); !errors.Is(err, boom) {
		t.Fatalf("apply changed: %v", err)
	}
	if n := versionCount(t, s); n != 1 {
		t.Fatalf("%d versions after a failed record", n)
	}
	if _, err := s.RestoreRecorded(t.Context(), k, 1, 1, a, rec.record); !errors.Is(err, boom) {
		t.Fatalf("restore: %v", err)
	}
	if n := versionCount(t, s); n != 1 {
		t.Fatalf("%d versions after a failed restore record", n)
	}
}

// A save that writes no version records nothing: a conflict, an unchanged repeat, a refusal.
func TestVersionRecordNotCalledWithoutANewVersion(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "first"), a); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	stale := versionRequest(0, "stale")
	stale.Record = rec.record
	if _, err := s.Apply(t.Context(), k, stale, a); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale: %v", err)
	}
	same := versionRequest(1, "first")
	same.Record = rec.record
	if _, unchanged, err := s.ApplyChanged(t.Context(), k, same, a); err != nil || !unchanged {
		t.Fatalf("unchanged: %v %v", unchanged, err)
	}
	refused := versionRequest(1, "other")
	refused.Record = rec.record
	deny := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("denied") }
	if _, err := s.Apply(t.Context(), k, refused, deny); err == nil {
		t.Fatal("refused apply succeeded")
	}
	if _, err := s.RestoreRecorded(t.Context(), k, 0, 1, a, rec.record); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale restore: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("recorded without a version: %v", rec.calls)
	}
}

// A restore records the version it appends.
func TestVersionRestoreRecordedRecordsTheNewVersion(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	for i, c := range []string{"first", "second"} {
		if _, err := s.Apply(t.Context(), k, versionRequest(int64(i), c), a); err != nil {
			t.Fatal(err)
		}
	}
	rec := &recorder{}
	got, err := s.RestoreRecorded(t.Context(), k, 2, 1, a, rec.record)
	if err != nil || got.Version != 3 || len(rec.calls) != 1 || rec.calls[0] != 3 || rec.inTxRows[0] != 1 {
		t.Fatalf("restore: %+v %v calls %v rows %v", got, err, rec.calls, rec.inTxRows)
	}
}
