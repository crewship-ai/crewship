package work

import (
	"errors"
	"testing"
)

func TestMarkRuntimeRequestedTxRemainsUnpublishedUntilCallerCommit(t *testing.T) {
	store, db, _ := newTestStore(t)
	accepted := accept(t, store, db, AcceptRequest{WorkspaceID: "ws1", Source: SourceWebhook, Class: ClassBackground, AgentID: "a"})
	claimed, err := store.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.MarkStarting(t.Context(), accepted.WorkID, claimed.RunID, claimed.Generation, "location"); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		var phase string
		if err := db.QueryRow(`SELECT runtime_phase FROM work_attempts WHERE run_id=?`, claimed.RunID).Scan(&phase); err != nil || phase != want {
			t.Fatalf("phase=%s want=%s err=%v", phase, want, err)
		}
	}
	check("starting")
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = MarkRuntimeRequestedTx(t.Context(), tx, accepted.WorkID, claimed.RunID, claimed.Generation); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check("starting")
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = MarkRuntimeRequestedTx(t.Context(), tx, accepted.WorkID, claimed.RunID, claimed.Generation+1); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale generation: %v", err)
	}
	if err = MarkRuntimeRequestedTx(t.Context(), tx, accepted.WorkID, claimed.RunID, claimed.Generation); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check("requested")
	if err = MarkRuntimeRequestedTx(t.Context(), nil, accepted.WorkID, claimed.RunID, claimed.Generation); !errors.Is(err, ErrNotBound) {
		t.Fatalf("nil transaction: %v", err)
	}
}
