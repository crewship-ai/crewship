package pipeline

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// Second-reader counterexample: a resume plan built while the run waited at
// E1 must not re-execute S after another resumer already consumed E1, ran S
// and parked the run at E2.
func TestSecondReader_StalePlanDoesNotReplayConsumedSignal(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('ws_test','Test','test')`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	in := validSaveInput("stale-plan")
	in.Author = AuthorMeta{Via: AuthoredViaUser}
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"stale-plan","steps":[` +
		`{"id":"e1","type":"wait","wait":{"kind":"event","event_type":"a"},"timeout_seconds":600},` +
		`{"id":"s","type":"code","code":{"runtime":"cel","code":"true"}},` +
		`{"id":"e2","type":"wait","wait":{"kind":"event","event_type":"b"},"timeout_seconds":600}]}`
	p, err := store.Save(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	side := &recordingCodeRunner{}
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).WithRunStore(NewRunStore(db)).WithRunRegistry(NewRunRegistry()).WithSignalRegistry(NewSignalRegistry()).WithSignalWaitStore(NewSQLSignalWaitStore(db)).WithCodeRunner(side)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park e1: %+v %v", res, err)
	}
	rec, err := exec.runStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	stalePlan, reason := exec.buildResumePlan(ctx, rec) // resumer B, e.g. backing off on a busy concurrency slot
	if stalePlan == nil {
		t.Fatal(reason)
	}
	stalePlan.reason = resumeReasonSignal
	if ok, err := exec.signalWaits.Deliver(ctx, res.RunID, "a", "payload"); !ok || err != nil {
		t.Fatalf("deliver: %v %v", ok, err)
	}
	exec.ResumeEventRun(ctx, res.RunID, log) // resumer A wins
	rec, err = exec.runStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != RunStatusWaiting || rec.CurrentStepID != "e2" || atomic.LoadInt32(&side.calls) != 1 {
		t.Fatalf("precondition: status=%s step=%s calls=%d", rec.Status, rec.CurrentStepID, atomic.LoadInt32(&side.calls))
	}
	exec.runResumedRun(ctx, stalePlan, log) // B's retry after A released the slot
	rec, _ = exec.runStore.Get(ctx, res.RunID)
	if n := atomic.LoadInt32(&side.calls); n != 1 {
		t.Fatalf("side-effect step ran %d times after stale resume (status=%s step=%s)", n, rec.Status, rec.CurrentStepID)
	}
}
