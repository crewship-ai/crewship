package pipeline

import (
	"context"
	"testing"
	"time"
)

// #5348: a step inside foreach that blocks on a person's decision (a held
// crewship action; `wait` is not allowed inside foreach) tells the caller through
// RunInput.OnHumanWait, with the run, the step and the pending token. The
// synchronous run endpoint uses it to hand the client a WAITING receipt
// instead of holding the run hostage to the request.
func TestOnHumanWait_NestedHeldActionAndApproval(t *testing.T) {
	for _, tc := range []struct {
		name, dsl, step string
	}{
		{"held crewship action in foreach", `{"dsl_version":"1.0","name":"hold-each","steps":[
 {"id":"each","type":"foreach","foreach":{"items":"[\"a\"]","as":"host","parallelism":1,"steps":[
  {"id":"create","type":"crewship","action":"issue.create","args":{"title":"Status {{ inputs.host }}"}}
 ]}}
]}`, "create"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openResumeTestDB(t)
			seedHeldActionMembers(t, db)
			t.Cleanup(func() { db.Close() })
			store := NewStore(db)
			wp := NewSQLWaitpointStore(db)
			t.Cleanup(wp.Close)
			p := saveResumePipeline(t, store, "signal", tc.dsl)
			exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
				WithRunStore(NewRunStore(db)).WithWaitpointStore(wp).WithCrewshipActions(&heldCrewshipActions{db: db})

			type signal struct{ runID, stepID, token string }
			signals := make(chan signal, 4)
			finished := make(chan *RunResult, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			go func() {
				res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun,
					OnHumanWait: func(runID, stepID, token string) { signals <- signal{runID, stepID, token} }})
				if err != nil {
					t.Errorf("run: %v", err)
				}
				finished <- res
			}()
			var got signal
			select {
			case got = <-signals:
			case res := <-finished:
				t.Fatalf("run ended without signalling a human wait: %+v", res)
			case <-ctx.Done():
				t.Fatal("no OnHumanWait signal while the step blocked on a person")
			}
			var pending, runOf string
			if err := db.QueryRow(`SELECT token, pipeline_run_id FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&pending, &runOf); err != nil {
				t.Fatal(err)
			}
			if got.token != pending || got.runID != runOf || got.stepID != tc.step {
				t.Fatalf("signal %+v, want token %s run %s step %s", got, pending, runOf, tc.step)
			}
			if err := wp.CompleteApproval(ctx, "ws_test", pending, true, "u_admin", ""); err != nil {
				t.Fatal(err)
			}
			if res := <-finished; res == nil || res.Status != "COMPLETED" {
				t.Fatalf("run after approval: %+v", res)
			}
		})
	}
}
