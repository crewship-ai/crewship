package appengine

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

func gridcraftRows(t *testing.T, s *Store) [5]int {
	t.Helper()
	var n [5]int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM gridcraft_heads),(SELECT count(*) FROM gridcraft_revisions),(SELECT count(*) FROM gridcraft_audit),(SELECT count(*) FROM gridcraft_outbox),(SELECT count(*) FROM gridcraft_operations)`).Scan(&n[0], &n[1], &n[2], &n[3], &n[4]); err != nil {
		t.Fatal(err)
	}
	return n
}

// #4835: a GridCraft write below the free-disk reserve is refused with the
// shared storage refusal before the engine runs. Nothing is written, the
// confirmed head stays, reads and an exact retry of a confirmed receipt keep
// answering, and the same request saves once space returns.
func TestGridCraftWriteBelowFreeDiskReserveIsRefusedBeforeEngine(t *testing.T) {
	s, db, auth, k := fixture(t)
	engine := &fakeEngine{}
	first := imported(t, s, auth, k, engine)
	before := gridcraftRows(t, s)
	head, err := s.Read(t.Context(), k, auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE artifact_storage_global SET min_free_bytes=? WHERE id=1`, int64(1)<<62); err != nil {
		t.Fatal(err)
	}
	calls := engine.calls.Load()
	if _, err = s.Execute(t.Context(), k, edit(), auth, engine); !errors.Is(err, storageheadroom.ErrInsufficient) {
		t.Fatalf("edit below reserve = %v, want free-disk refusal", err)
	}
	if engine.calls.Load() != calls {
		t.Fatal("refused edit still ran the engine")
	}
	if got := gridcraftRows(t, s); got != before {
		t.Fatalf("refused edit wrote rows: before=%v after=%v", before, got)
	}
	again, err := s.Read(t.Context(), k, auth)
	if err != nil || again.Rev != head.Rev || string(again.Snapshot) != string(head.Snapshot) {
		t.Fatalf("read below reserve = %+v %v, want unchanged %+v", again, err, head)
	}
	replay, err := s.Execute(t.Context(), k, Request{MutationID: "import-1", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: []byte(`{"sheets":[{}]}`)}}, auth, engine)
	if err != nil || replay.Rev != first.Rev {
		t.Fatalf("exact retry of a confirmed receipt below reserve = %+v %v", replay, err)
	}
	if _, err = s.Status(t.Context(), k, "import-1", auth); err != nil {
		t.Fatalf("status below reserve: %v", err)
	}
	if _, err = db.Exec(`UPDATE artifact_storage_global SET min_free_bytes=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Execute(t.Context(), k, edit(), auth, engine); err != nil || r.Rev != 2 {
		t.Fatalf("same edit after space returned = %+v %v", r, err)
	}
}

// The reserve can shrink while the engine prepares the candidate. The commit
// transaction probes again with the real snapshot size and writes nothing.
func TestGridCraftCommitBelowFreeDiskReserveWritesNothing(t *testing.T) {
	s, db, auth, k := fixture(t)
	imported(t, s, auth, k, &fakeEngine{})
	before := gridcraftRows(t, s)
	engine := &fakeEngine{}
	engine.prepare = func(ctx context.Context, b Binding, st State, c Command) (Candidate, error) {
		if _, err := db.ExecContext(ctx, `UPDATE artifact_storage_global SET min_free_bytes=? WHERE id=1`, int64(1)<<62); err != nil {
			return Candidate{}, err
		}
		return NewCandidate(b, []byte(`{"sheets":[{"B2":"42"}]}`), commandTargets(c)), nil
	}
	if _, err := s.Execute(t.Context(), k, edit(), auth, engine); !errors.Is(err, storageheadroom.ErrInsufficient) {
		t.Fatalf("commit below reserve = %v, want free-disk refusal", err)
	}
	after := gridcraftRows(t, s)
	// The reservation row is the existing per-attempt fence; it is released
	// as failed, exactly as for any other refused commit.
	before[4]++
	if after != before {
		t.Fatalf("refused commit wrote content rows: before=%v after=%v", before, after)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM gridcraft_operations WHERE mutation_id='edit-1'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("reservation state=%q err=%v", state, err)
	}
	if v, err := s.Read(t.Context(), k, auth); err != nil || v.Rev != 1 {
		t.Fatalf("confirmed head changed: %+v %v", v, err)
	}
}
