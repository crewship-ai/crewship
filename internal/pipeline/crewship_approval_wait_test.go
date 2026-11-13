package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
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
	key := CrewshipApprovalStepKey(req.StepID, CrewshipApprovalBinding(req.OperationKey, req.Verb, req.Args))
	if err := h.db.QueryRowContext(ctx, `SELECT status FROM pipeline_waitpoints WHERE token = ? AND pipeline_run_id = ? AND step_id = ? AND kind = 'approval'`,
		req.ApprovalToken, req.RunID, key).Scan(&status); err != nil {
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
	// Every attempt carried the same identity (operation key and step), so
	// the approval's binding matches the call that executes.
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

// One approval answers one call. Inside a foreach every item has its own
// operation key, so approving the first item must not unlock the second:
// each gets its own card, and each executes once after its own approval.
func TestCrewshipStep_ApprovalIsPerForeachItem(t *testing.T) {
	db := openResumeTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewStore(db)
	runStore := NewRunStore(db)
	wp := NewSQLWaitpointStore(db)
	t.Cleanup(wp.Close)
	p := saveResumePipeline(t, store, "split-each", `{"dsl_version":"1.0","name":"split-each","steps":[
 {"id":"each","type":"foreach","foreach":{"items":"[\"a\",\"b\"]","as":"host","parallelism":1,"steps":[
  {"id":"create","type":"crewship","action":"issue.create","args":{"title":"Status {{ inputs.host }}"}}
 ]}}
]}`)
	actions := &heldCrewshipActions{db: db}
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore).WithWaitpointStore(wp).WithCrewshipActions(actions)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Inside a foreach the step blocks on its approval instead of parking
	// the run, so a person answers each card while the run is live.
	type outcome struct {
		res *RunResult
		err error
	}
	finished := make(chan outcome, 1)
	go func() {
		res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
		finished <- outcome{res, err}
	}()
	approved := map[string]bool{}
	for len(approved) < 2 {
		var token string
		err := db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE status = 'pending' ORDER BY created_at LIMIT 1`).Scan(&token)
		if err == nil && !approved[token] {
			actions.mu.Lock()
			ran := len(actions.done)
			actions.mu.Unlock()
			if ran != len(approved) {
				t.Fatalf("%d actions ran after %d approvals; one approval must unlock one item", ran, len(approved))
			}
			if err := wp.CompleteApproval(ctx, "ws_test", token, true, "u_admin", ""); err != nil {
				t.Fatal(err)
			}
			approved[token] = true
			continue
		}
		select {
		case o := <-finished:
			t.Fatalf("run ended after %d approvals: %+v %v", len(approved), o.res, o.err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for the next approval card")
		case <-time.After(20 * time.Millisecond):
		}
	}
	o := <-finished
	if o.err != nil || o.res.Status != "COMPLETED" {
		t.Fatalf("run: %v %+v", o.err, o.res)
	}
	if len(actions.done) != 2 || actions.done[0] == actions.done[1] {
		t.Fatalf("done=%v, want two distinct executions", actions.done)
	}
	var cards int
	db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints`).Scan(&cards)
	if cards != 2 {
		t.Fatalf("approval cards = %d, want one per item", cards)
	}
}

// The binding changes with the operation key (item), the verb and every
// argument.
func TestCrewshipApprovalBinding(t *testing.T) {
	base := CrewshipApprovalBinding("op-1", "issue.create", map[string]any{"title": "a", "labels": []any{"x"}})
	if again := CrewshipApprovalBinding("op-1", "issue.create", map[string]any{"labels": []any{"x"}, "title": "a"}); again != base {
		t.Fatal("binding depends on map order")
	}
	for name, other := range map[string]string{
		"item": CrewshipApprovalBinding("op-2", "issue.create", map[string]any{"title": "a", "labels": []any{"x"}}),
		"verb": CrewshipApprovalBinding("op-1", "issue.update", map[string]any{"title": "a", "labels": []any{"x"}}),
		"args": CrewshipApprovalBinding("op-1", "issue.create", map[string]any{"title": "b", "labels": []any{"x"}}),
	} {
		if other == base {
			t.Errorf("binding ignores the %s", name)
		}
	}
}

// Long arguments are never cut silently: the card says they were truncated
// and names the binding the approval is tied to.
func TestArgsForPrompt(t *testing.T) {
	if got := argsForPrompt([]byte(`{"a":1}`), "b1"); got != "```json\n{\"a\":1}\n```" {
		t.Fatalf("short args = %q", got)
	}
	// Author-controlled backticks cannot close the fence early.
	if got := argsForPrompt([]byte("{\"t\":\"```[x](y)\"}"), "b1"); !strings.HasPrefix(got, "````json\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("fence not widened: %q", got)
	}
	// The cut never splits a multi-byte character.
	multi := []byte("{\"t\":\"" + strings.Repeat("ž", approvalArgsShown) + "\"}")
	if got := argsForPrompt(multi, "b1"); !utf8.ValidString(got) {
		t.Fatal("truncation split a UTF-8 character")
	}
	long := []byte("{\"body\":\"" + strings.Repeat("x", approvalArgsShown) + "\"}")
	got := argsForPrompt(long, "b1")
	if !strings.Contains(got, "Arguments truncated") || !strings.Contains(got, "b1") {
		t.Fatalf("long args not marked: %q", got[len(got)-200:])
	}
}

// A held crewship action is decided by a person every time: a standing trust
// grant for the very same gate neither approves it nor is spent by it, and
// the card offers no new grant.
func TestCreateApproval_NoStandingTrust(t *testing.T) {
	ctx := context.Background()
	db := openTrustGateTestDB(t)
	grants := NewTrustGrantStore(db)
	mustGrant(t, grants, baseGrant())
	seedTrustRun(t, db, "run_held", "hashA", "guided")
	store := NewSQLWaitpointStore(db)
	defer store.Close()

	token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID: "ws_test", PipelineRunID: "run_held", StepID: "publish",
		Prompt: "Approve issue.create", NoStandingTrust: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ := waitpointRow(t, db, token); status != "pending" {
		t.Fatalf("status = %q, want pending: a standing grant approved a held action", status)
	}
	var uses int
	if err := db.QueryRow(`SELECT uses FROM waitpoint_trust_grants`).Scan(&uses); err != nil || uses != 0 {
		t.Fatalf("grant uses = %d (%v), want 0: the grant was spent", uses, err)
	}
	var payload string
	if err := db.QueryRow(`SELECT COALESCE(payload_json, '') FROM inbox_items WHERE source_id = ?`, token).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, `"eligible":true`) {
		t.Fatalf("the card offers a standing grant: %s", payload)
	}
}

// With no grant yet, a guided crew's approval card offers one. A held
// crewship action's card must not: the control case proves the offer is
// otherwise there, so the assertion fails if the guard is removed.
func TestCreateApproval_NoStandingTrustOffer(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait step offers", true: "held action does not"}[held], func(t *testing.T) {
			ctx := context.Background()
			db := openTrustGateTestDB(t)
			seedTrustRun(t, db, "run_offer", "hashA", "guided")
			// Enough earlier human approvals of this step to earn an offer.
			for i := 0; i < TrustOfferThreshold; i++ {
				if _, err := db.Exec(`INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at)
VALUES (?, 'ws_test', 'run_offer', 'publish', 'approval', 'approved', datetime('now'))`, fmt.Sprintf("prior-%d", i)); err != nil {
					t.Fatal(err)
				}
			}
			store := NewSQLWaitpointStore(db)
			defer store.Close()
			token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
				WorkspaceID: "ws_test", PipelineRunID: "run_offer", StepID: "publish",
				Prompt: "Approve", NoStandingTrust: held,
			})
			if err != nil {
				t.Fatal(err)
			}
			var payload string
			if err := db.QueryRow(`SELECT COALESCE(payload_json, '') FROM inbox_items WHERE source_id = ?`, token).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			if offered := strings.Contains(payload, `"eligible":true`); offered == held {
				t.Fatalf("held=%v offered=%v: %s", held, offered, payload)
			}
		})
	}
}

// A held crewship action's approval is decided only by a current workspace
// OWNER or ADMIN: the public callback (external holder), a MANAGER and a
// member of another workspace are refused and leave it pending. An ordinary
// wait step's approval keeps its audience (MANAGER, the external holder).
func TestDecide_HeldActionNeedsOwnerOrAdmin(t *testing.T) {
	ctx := context.Background()
	held := CrewshipApprovalStepKey("create", CrewshipApprovalBinding("op", "issue.create", map[string]any{"title": "x"}))
	cases := []struct {
		name    string
		stepID  string
		decider WaitpointDecider
		allowed bool
	}{
		{"admin approves a held action", held, WaitpointDecider{Kind: DeciderUser, ID: "u_admin"}, true},
		{"owner approves a held action", held, WaitpointDecider{Kind: DeciderUser, ID: "u_owner"}, true},
		{"manager cannot", held, WaitpointDecider{Kind: DeciderUser, ID: "u_manager"}, false},
		{"admin of another workspace cannot", held, WaitpointDecider{Kind: DeciderUser, ID: "u_elsewhere"}, false},
		{"public callback cannot", held, WaitpointDecider{Kind: DeciderExternal, ID: "external-callback"}, false},
		{"wait step: manager still can", "gate", WaitpointDecider{Kind: DeciderUser, ID: "u_manager"}, true},
		{"wait step: public callback still can", "gate", WaitpointDecider{Kind: DeciderExternal, ID: "external-callback"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTrustGateTestDB(t)
			seedTrustRun(t, db, "run_d", "hashA", "strict")
			if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS workspace_members (workspace_id TEXT, user_id TEXT, role TEXT)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES
 ('ws_test','u_admin','ADMIN'), ('ws_test','u_owner','owner'), ('ws_test','u_manager','MANAGER'), ('ws_other','u_elsewhere','ADMIN')`); err != nil {
				t.Fatal(err)
			}
			store := NewSQLWaitpointStore(db)
			defer store.Close()
			token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
				WorkspaceID: "ws_test", PipelineRunID: "run_d", StepID: tc.stepID, Prompt: "Approve", NoStandingTrust: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantRole := map[bool]string{true: "ADMIN", false: "MANAGER"}[IsCrewshipApprovalStepKey(tc.stepID)]
			var role string
			if err := db.QueryRow(`SELECT target_role FROM inbox_items WHERE source_id = ?`, token).Scan(&role); err != nil || role != wantRole {
				t.Fatalf("card addressed to %q (%v), want %q: the audience must be who can decide", role, err, wantRole)
			}
			err = store.Decide(ctx, "ws_test", token, true, tc.decider, "")
			status, _, _ := waitpointRow(t, db, token)
			if tc.allowed {
				if err != nil || status != "approved" {
					t.Fatalf("err=%v status=%q, want approved", err, status)
				}
				return
			}
			if !errors.Is(err, ErrDeciderNotAllowed) || status != "pending" {
				t.Fatalf("err=%v status=%q, want ErrDeciderNotAllowed and still pending", err, status)
			}
		})
	}
}
