package documents

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
)

// T-OPS-01: replay capacity must not become a permanent document lock.
func TestReplayPressureCompactsBeforeRejectingValidEdit(t *testing.T) {
	for _, tt := range []struct {
		name                           string
		limit, firstBytes, secondBytes int
	}{
		{"configured small budget", 15, 11, 11},
		{"default budget", 0, 60 << 10, 250 << 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{MaxLogBytes: tt.limit})
			defer db.Close()
			lease := acquire(t, s)
			if _, err := s.Checkpoint(ctx, key, lease, "pinned", []byte("pinned state"), 0, allow); err != nil {
				t.Fatal(err)
			}
			first := commit(lease, "first", 0)
			first.Update = bytes.Repeat([]byte("a"), tt.firstBytes)
			if _, err := s.Commit(ctx, key, first, allow); err != nil {
				t.Fatal(err)
			}
			second := commit(lease, "second", 1)
			second.Update = bytes.Repeat([]byte("b"), tt.secondBytes)
			receipt, err := s.Commit(ctx, key, second, allow)
			if err != nil {
				t.Fatalf("accumulated replay bytes permanently lock valid edit: %v", err)
			}
			state, err := s.Read(ctx, key, allow)
			if err != nil || state.HeadRev != 2 || state.SnapshotRev != 2 || len(state.Updates) != 0 || string(state.Snapshot) != "candidate" {
				t.Fatalf("pressure compaction did not retain durable candidate: %+v %v", state, err)
			}
			pinned, err := s.CheckpointRead(ctx, key, "pinned", allow)
			if err != nil || string(pinned.Snapshot) != "pinned state" {
				t.Fatalf("lost checkpoint: %+v %v", pinned, err)
			}
			replay, err := s.Commit(ctx, key, second, allow)
			if err != nil || replay != receipt {
				t.Fatalf("compaction lost retry receipt: %+v %v", replay, err)
			}
			first.Update = []byte("changed")
			if _, err := s.Commit(ctx, key, first, allow); !errors.Is(err, ErrDuplicate) {
				t.Fatalf("compaction lost original request identity: %v", err)
			}
			if _, err := s.Commit(ctx, key, commit(lease, "continues", 2), allow); err != nil {
				t.Fatalf("editing did not resume: %v", err)
			}
		})
	}
}

func TestReplayPressureCompactionRollsBackOnStorageFailure(t *testing.T) {
	for _, trigger := range []struct{ name, sql string }{
		{"snapshot", "CREATE TRIGGER fail_pressure BEFORE UPDATE OF snapshot ON documents BEGIN SELECT RAISE(ABORT, 'pressure snapshot failure'); END"},
		{"log cleanup", "CREATE TRIGGER fail_pressure BEFORE DELETE ON document_updates BEGIN SELECT RAISE(ABORT, 'pressure cleanup failure'); END"},
		{"head", "CREATE TRIGGER fail_pressure BEFORE UPDATE OF head_rev ON documents BEGIN SELECT RAISE(ABORT, 'pressure head failure'); END"},
	} {
		t.Run(trigger.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{MaxLogBytes: 15})
			defer db.Close()
			lease := acquire(t, s)
			if _, err := s.Commit(ctx, key, commit(lease, "first", 0), allow); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(trigger.sql); err != nil {
				t.Fatal(err)
			}
			_, err := s.Commit(ctx, key, commit(lease, "pressure", 1), allow)
			if err == nil || errors.Is(err, ErrQuota) {
				t.Fatalf("did not reach injected compaction failure: %v", err)
			}
			state, err := s.Read(ctx, key, allow)
			if err != nil || state.HeadRev != 1 || state.SnapshotRev != 0 || len(state.Updates) != 1 || string(state.Snapshot) != "baseline" {
				t.Fatalf("failed compaction lost original replay: %+v %v", state, err)
			}
			if _, found, err := s.LookupReceipt(ctx, key, "pressure", "semantic", allow); err != nil || found {
				t.Fatalf("failed compaction retained false receipt: %v %v", found, err)
			}
			if _, err := db.Exec("DROP TRIGGER fail_pressure"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Commit(ctx, key, commit(lease, "pressure", 1), allow); err != nil {
				t.Fatalf("retry after storage repair: %v", err)
			}
		})
	}
}

func TestReplayPressureCannotBypassCurrentAuthorityOrRevision(t *testing.T) {
	s, db, _ := fixture(t, Options{MaxLogBytes: 15})
	defer db.Close()
	lease := acquire(t, s)
	if _, err := s.Commit(ctx, key, commit(lease, "first", 0), allow); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("current authority revoked")
	deny := func(context.Context, *sql.Tx) (string, error) { return "", denied }
	if _, err := s.Commit(ctx, key, commit(lease, "denied", 1), deny); !errors.Is(err, denied) {
		t.Fatalf("pressure bypassed authorization: %v", err)
	}
	if _, err := s.Commit(ctx, key, commit(lease, "stale", 0), allow); !errors.Is(err, ErrConflict) {
		t.Fatalf("pressure bypassed revision: %v", err)
	}
	state, err := s.Read(ctx, key, allow)
	if err != nil || state.HeadRev != 1 || state.SnapshotRev != 0 || len(state.Updates) != 1 {
		t.Fatalf("denied operation compacted state: %+v %v", state, err)
	}
}
