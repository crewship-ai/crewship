package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

// Fault injection through the actual /assign producer must roll back the
// domain insert as well as its work admission, including standalone DB callers.
func TestAssignmentWorkReviewAcceptanceIsAtomic(t *testing.T) {
	f := setupDelegationFixture(t)
	execOrFatal(t, f.db, `CREATE TRIGGER reject_work BEFORE INSERT ON work_items BEGIN SELECT RAISE(ABORT,'injected work failure'); END`)
	r := f.assign(t, f.lead, "w1", nil)
	if r.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
	}
	if n := assignmentCount(t, f); n != 0 {
		t.Fatalf("failed acceptance retained %d assignments", n)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("work=%d err=%v", count, err)
	}
	execOrFatal(t, f.db, `DROP TRIGGER reject_work`)
	r = f.assign(t, f.lead, "w1", nil)
	if r.Code != http.StatusCreated && r.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", r.Code, r.Body.String())
	}
	f.h.WaitDispatches()
	if n := assignmentCount(t, f); n != 1 {
		t.Fatalf("retry retained %d assignments", n)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM work_items w JOIN assignments a ON a.id=w.domain_id AND a.workspace_id=w.workspace_id WHERE w.source='assignment' AND w.domain_kind='assignment' AND w.agent_id=a.assigned_to_id`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("owned work=%d err=%v", count, err)
	}
	claimed, err := work.NewStore(f.db).Claim(context.Background(), work.ClaimOptions{LeaseOwner: "review", AgentID: f.work1})
	if err != nil {
		t.Fatal(err)
	}
	memory := &MemoryMutationHandler{db: f.db}
	if err := memory.authorizeRun(f.wsID, f.work1, claimed.RunID, claimed.Generation)(context.Background()); err != nil {
		t.Fatalf("actual assignment producer did not authorize current memory attempt: %v", err)
	}
	for _, bad := range []struct {
		agent      string
		generation int64
	}{{f.work2, claimed.Generation}, {f.work1, claimed.Generation + 1}} {
		if err := memory.authorizeRun(f.wsID, bad.agent, claimed.RunID, bad.generation)(context.Background()); err == nil {
			t.Fatalf("foreign/stale memory authority accepted: %+v", bad)
		}
	}
}

func TestAssignmentWorkReviewRechecksImmutableAuthority(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE assignments SET task='replacement task' WHERE id='review'`,
		`UPDATE assignments SET assigned_to_id='w2' WHERE id='review'`,
		`UPDATE assignments SET assigned_by_id='w2' WHERE id='review'`,
		`UPDATE assignments SET created_by_user_id=NULL WHERE id='review'`,
		`UPDATE agents SET deleted_at=datetime('now') WHERE id='leadD'`,
		`DELETE FROM workspace_members`,
		`UPDATE crews SET deleted_at=datetime('now') WHERE id='crewD'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			f := setupDelegationFixture(t)
			var user string
			if err := f.db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id=?`, f.wsID).Scan(&user); err != nil {
				t.Fatal(err)
			}
			f.insertRootDispatch(t, "review", f.lead, f.work1, "PENDING")
			execOrFatal(t, f.db, `UPDATE assignments SET created_by_user_id=? WHERE id='review'`, user)
			tx, err := f.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := acceptAssignmentTx(context.Background(), tx, "review")
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			store := work.NewStore(f.db)
			claimed, err := store.Claim(context.Background(), work.ClaimOptions{LeaseOwner: "review", WorkID: receipt.WorkID})
			if err != nil {
				t.Fatal(err)
			}
			a := dispatch.Assignment{Item: claimed.Item, RunID: claimed.RunID, Attempt: claimed.Attempt, Generation: claimed.Generation}
			auth := assignmentWorkAuthorizer{h: f.h}
			if d, err := auth.Authorize(context.Background(), a); err != nil || d != dispatch.Allow() {
				t.Fatalf("baseline decision=%v err=%v", d, err)
			}
			execOrFatal(t, f.db, mutation)
			if d, err := auth.Authorize(context.Background(), a); err != nil || d == dispatch.Allow() {
				t.Fatalf("replacement authorized: decision=%v err=%v", d, err)
			}
		})
	}
}
