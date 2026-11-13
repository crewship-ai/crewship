package pipeline

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
)

// A strict crew's routine used to fail at its crewship step with "a routine
// cannot wait for an approval". The step now parks on an ordinary approval
// waitpoint; an approval runs the action exactly once with the same
// operation key, and a denial fails the run. The autonomy level is never
// changed and no standing grant is involved.

// heldCrewshipActions mimics the dispatcher's contract: held unless the
// request carries the token of an APPROVED approval waitpoint of this run and
// step, read from the database the executor wrote it to.
type heldCrewshipActions struct {
	db    *sql.DB
	mu    sync.Mutex
	calls []CrewshipRequest
	done  []string // operation keys that actually executed
}

func (h *heldCrewshipActions) Do(ctx context.Context, req CrewshipRequest) (string, error) {
	h.mu.Lock()
	h.calls = append(h.calls, req)
	h.mu.Unlock()
	held := &CrewshipApprovalRequired{Verb: req.Verb, Action: "mission_create", Level: "strict", CrewID: "crew_strict", Decision: "inbox_approve"}
	if req.ApprovalToken == "" {
		return "", held
	}
	var status string
	if err := h.db.QueryRowContext(ctx, `SELECT status FROM pipeline_waitpoints WHERE token = ? AND pipeline_run_id = ? AND step_id = ? AND kind = 'approval'`,
		req.ApprovalToken, req.RunID, req.StepID).Scan(&status); err != nil {
		return "", err
	}
	if status != "approved" {
		return "", held
	}
	h.mu.Lock()
	h.done = append(h.done, req.OperationKey)
	h.mu.Unlock()
	return `{"identifier":"DEV-1"}`, nil
}

const crewshipApprovalDSL = `{"dsl_version":"1.0","name":"split","steps":[
 {"id":"probe","type":"transform","transform":{"input":"ok","expression":"."}},
 {"id":"create","type":"crewship","action":"issue.create","args":{"title":"Host A status"}}
]}`

func runHeldCrewship(t *testing.T) (*Executor, *RunStore, *SQLWaitpointStore, *heldCrewshipActions, *RunResult, *sql.DB) {
	t.Helper()
	db := openResumeTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	runStore := NewRunStore(db)
	wp := NewSQLWaitpointStore(db)
	t.Cleanup(wp.Close)
	p := saveResumePipeline(t, store, "split", crewshipApprovalDSL)
	actions := &heldCrewshipActions{db: db}
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore).WithWaitpointStore(wp).WithCrewshipActions(actions)
	res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return exec, runStore, wp, actions, res, db
}

func TestCrewshipStep_HeldDecisionParksOnApproval(t *testing.T) {
	_, runStore, _, actions, res, db := runHeldCrewship(t)
	if res.Status != "WAITING" || res.CurrentStep != "create" || res.WaitpointToken == "" {
		t.Fatalf("status=%s step=%s token=%q, want WAITING on create with a token (%+v)", res.Status, res.CurrentStep, res.WaitpointToken, res)
	}
	rec, _ := runStore.Get(context.Background(), res.RunID)
	if rec.Status != RunStatusWaiting {
		t.Fatalf("run row = %s, want waiting", rec.Status)
	}
	var prompt string
	if err := db.QueryRow(`SELECT COALESCE(prompt,'') FROM pipeline_waitpoints WHERE token = ?`, res.WaitpointToken).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "issue.create") || !strings.Contains(prompt, "Host A status") {
		t.Fatalf("approval prompt must name the action and its arguments: %q", prompt)
	}
	if len(actions.done) != 0 {
		t.Fatal("the held action executed before anyone approved it")
	}
}

func TestCrewshipStep_ApprovalExecutesExactlyOnce(t *testing.T) {
	exec, runStore, wp, actions, res, db := runHeldCrewship(t)
	ctx := context.Background()

	// A resume while still pending parks again without a second card.
	resumeAndAwait(t, exec, runStore, res.RunID)
	if rec, _ := runStore.Get(ctx, res.RunID); rec.Status != RunStatusWaiting {
		t.Fatalf("resume before a decision: status %s, want still waiting", rec.Status)
	}
	var cards int
	db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE pipeline_run_id = ?`, res.RunID).Scan(&cards)
	if cards != 1 {
		t.Fatalf("approval cards = %d, want 1", cards)
	}

	if err := wp.CompleteApproval(ctx, "ws_test", res.WaitpointToken, true, "u_admin", ""); err != nil {
		t.Fatal(err)
	}
	resumeAndAwait(t, exec, runStore, res.RunID)
	rec, _ := runStore.Get(ctx, res.RunID)
	if rec.Status != RunStatusCompleted {
		t.Fatalf("after approval: status %s (%s), want completed", rec.Status, rec.ErrorMessage)
	}
	if len(actions.done) != 1 {
		t.Fatalf("action executed %d times, want exactly once", len(actions.done))
	}
	// Every attempt carried the same operation key, so a lost response
	// replays rather than duplicates on the server.
	for _, c := range actions.calls {
		if c.OperationKey != actions.calls[0].OperationKey || c.StepID != "create" {
			t.Fatalf("attempts disagree on identity: %+v", actions.calls)
		}
	}
}

func TestCrewshipStep_DeniedApprovalFailsWithoutExecuting(t *testing.T) {
	exec, runStore, wp, actions, res, _ := runHeldCrewship(t)
	ctx := context.Background()
	if err := wp.CompleteApproval(ctx, "ws_test", res.WaitpointToken, false, "u_admin", "no"); err != nil {
		t.Fatal(err)
	}
	resumeAndAwait(t, exec, runStore, res.RunID)
	rec, _ := runStore.Get(ctx, res.RunID)
	if rec.Status != RunStatusFailed || !strings.Contains(rec.ErrorMessage, "approval denied") {
		t.Fatalf("status=%s error=%q, want failed with approval denied", rec.Status, rec.ErrorMessage)
	}
	if len(actions.done) != 0 {
		t.Fatal("a denied action executed")
	}
}
