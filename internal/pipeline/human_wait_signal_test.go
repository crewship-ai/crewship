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

// #5354: a held action inside a CALLED routine signals the caller too, and the
// signal names the run the caller started (the parent), not the child's own
// run, so a synchronous run can return a receipt for the right run.
func TestOnHumanWait_NestedRoutineReportsTheParentRun(t *testing.T) {
	db := openResumeTestDB(t)
	seedHeldActionMembers(t, db)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	wp := NewSQLWaitpointStore(db)
	t.Cleanup(wp.Close)
	saveResumePipeline(t, store, "child-hold", `{"dsl_version":"1.0","name":"child-hold","steps":[
 {"id":"each","type":"foreach","foreach":{"items":"[\"a\"]","as":"host","parallelism":1,"steps":[
  {"id":"create","type":"crewship","action":"issue.create","args":{"title":"Status {{ inputs.host }}"}}
 ]}}
]}`)
	parent := saveResumePipeline(t, store, "parent-call", `{"dsl_version":"1.0","name":"parent-call","steps":[
 {"id":"call","type":"call_pipeline","pipeline_slug":"child-hold"}
]}`)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(NewRunStore(db)).WithWaitpointStore(wp).WithCrewshipActions(&heldCrewshipActions{db: db})

	type signal struct{ runID, stepID, token string }
	signals := make(chan signal, 4)
	started := make(chan string, 1)
	finished := make(chan *RunResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		res, err := exec.Run(ctx, RunInput{PipelineID: parent.ID, WorkspaceID: "ws_test", Mode: ModeRun,
			OnStarted:   func(id string) { started <- id },
			OnHumanWait: func(runID, stepID, token string) { signals <- signal{runID, stepID, token} }})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		finished <- res
	}()
	parentRun := <-started
	var got signal
	select {
	case got = <-signals:
	case res := <-finished:
		t.Fatalf("run ended without signalling a human wait: %+v", res)
	case <-ctx.Done():
		t.Fatal("a held action in a called routine never signalled the caller")
	}
	if got.runID != parentRun {
		t.Fatalf("signal names run %s, want the run the caller started (%s)", got.runID, parentRun)
	}
	var pending string
	if err := db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if got.token != pending {
		t.Fatalf("signal token %q, want %q", got.token, pending)
	}
	if err := wp.CompleteApproval(ctx, "ws_test", pending, true, "u_admin", ""); err != nil {
		t.Fatal(err)
	}
	if res := <-finished; res == nil || res.Status != "COMPLETED" {
		t.Fatalf("run after approval: %+v", res)
	}
}

// #5354: when the person denies the wait inside the called routine, the run the
// caller started ends FAILED (not hung, not COMPLETED), and a second decision
// is refused.
func TestOnHumanWait_NestedDenialEndsTheParentRunOnce(t *testing.T) {
	db := openResumeTestDB(t)
	seedHeldActionMembers(t, db)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	wp := NewSQLWaitpointStore(db)
	t.Cleanup(wp.Close)
	saveResumePipeline(t, store, "child-deny", `{"dsl_version":"1.0","name":"child-deny","steps":[
 {"id":"each","type":"foreach","foreach":{"items":"[\"a\"]","as":"host","parallelism":1,"steps":[
  {"id":"create","type":"crewship","action":"issue.create","args":{"title":"Status {{ inputs.host }}"}}
 ]}}
]}`)
	parent := saveResumePipeline(t, store, "parent-deny", `{"dsl_version":"1.0","name":"parent-deny","steps":[
 {"id":"call","type":"call_pipeline","pipeline_slug":"child-deny"}
]}`)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(NewRunStore(db)).WithWaitpointStore(wp).WithCrewshipActions(&heldCrewshipActions{db: db})
	signals := make(chan string, 4)
	finished := make(chan *RunResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		res, _ := exec.Run(ctx, RunInput{PipelineID: parent.ID, WorkspaceID: "ws_test", Mode: ModeRun,
			OnHumanWait: func(_, _, token string) { signals <- token }})
		finished <- res
	}()
	var token string
	select {
	case token = <-signals:
	case <-ctx.Done():
		t.Fatal("no signal")
	}
	var cards int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&cards); err != nil || cards != 1 {
		t.Fatalf("pending waitpoints = %d (%v), want exactly one card", cards, err)
	}
	if err := wp.CompleteApproval(ctx, "ws_test", token, false, "u_admin", "no"); err != nil {
		t.Fatal(err)
	}
	res := <-finished
	if res == nil || res.Status != "FAILED" {
		t.Fatalf("run after denial: %+v, want FAILED", res)
	}
	if err := wp.CompleteApproval(ctx, "ws_test", token, true, "u_admin", ""); err == nil {
		t.Fatal("a second decision was accepted")
	}
}

// #5354: a held action waiting inside a CALLED routine whose approval expires
// ends the run the caller started FAILED, with no card left pending (the slot
// is free), and the action never runs.
func TestOnHumanWait_NestedExpiryEndsTheParentRun(t *testing.T) {
	db := openResumeTestDB(t)
	seedHeldActionMembers(t, db)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	wp := NewSQLWaitpointStore(db)
	t.Cleanup(wp.Close)
	saveResumePipeline(t, store, "child-expire", `{"dsl_version":"1.0","name":"child-expire","steps":[
 {"id":"each","type":"foreach","foreach":{"items":"[\"a\"]","as":"host","parallelism":1,"steps":[
  {"id":"create","type":"crewship","action":"issue.create","timeout_seconds":1,"args":{"title":"Status {{ inputs.host }}"}}
 ]}}
]}`)
	parent := saveResumePipeline(t, store, "parent-expire", `{"dsl_version":"1.0","name":"parent-expire","steps":[
 {"id":"call","type":"call_pipeline","pipeline_slug":"child-expire"}
]}`)
	actions := &heldCrewshipActions{db: db}
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(NewRunStore(db)).WithWaitpointStore(wp).WithCrewshipActions(actions)
	signals := make(chan string, 4)
	finished := make(chan *RunResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	go func() {
		res, _ := exec.Run(ctx, RunInput{PipelineID: parent.ID, WorkspaceID: "ws_test", Mode: ModeRun,
			OnHumanWait: func(_, _, token string) { signals <- token }})
		finished <- res
	}()
	select {
	case <-signals:
	case <-ctx.Done():
		t.Fatal("no signal")
	}
	select {
	case res := <-finished:
		if res == nil || res.Status != "FAILED" {
			t.Fatalf("run after the approval expired: %+v, want FAILED", res)
		}
	case <-ctx.Done():
		t.Fatal("the run kept waiting after its approval expired")
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending waitpoints = %d (%v), want none", pending, err)
	}
}
