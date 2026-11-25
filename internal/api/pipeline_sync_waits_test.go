package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #5354: every synchronous routine entry point returns the WAITING receipt when
// a step starts waiting for a person, instead of holding the request open. The
// run continues without the request and a decision completes it once.

const syncWaitHeldDef = `{"dsl_version":"1.0","name":"held-each","steps":[
 {"id":"each","type":"foreach","foreach":{"items":"[\"a\"]","as":"host","parallelism":1,"steps":[
  {"id":"create","type":"crewship","action":"issue.create","args":{"title":"Status {{ inputs.host }}"}}
 ]}}
]}`

// syncWaitActions holds every call until its request carries the token of an
// approved waitpoint of the same run.
type syncWaitActions struct {
	db   *sql.DB
	mu   sync.Mutex
	done int
}

func (s *syncWaitActions) Do(ctx context.Context, req pipeline.CrewshipRequest) (string, error) {
	held := &pipeline.CrewshipApprovalRequired{Verb: req.Verb, Action: "mission_create", Level: "strict", CrewID: "held-crew", Decision: "inbox_approve"}
	if req.ApprovalToken == "" {
		return "", held
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM pipeline_waitpoints WHERE token = ? AND pipeline_run_id = ?`, req.ApprovalToken, req.RunID).Scan(&status); err != nil {
		return "", err
	}
	if status != "approved" {
		return "", held
	}
	s.mu.Lock()
	s.done++
	s.mu.Unlock()
	return `{"identifier":"HLD-1"}`, nil
}

func (s *syncWaitActions) executed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

type syncWaitRig struct {
	h       *PipelineHandler
	db      *sql.DB
	user    string
	ws      string
	wp      *pipeline.SQLWaitpointStore
	actions *syncWaitActions
}

func newSyncWaitRig(t *testing.T) *syncWaitRig {
	t.Helper()
	h, db, user, ws := runsHandlerRig(t)
	runs := pipeline.NewRunStore(db)
	h.SetRunStore(runs)
	h.SetRunRegistry(pipeline.NewRunRegistry())
	h.SetRunner(&stubRunner{output: "ok"})
	wp := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(wp.Close)
	h.SetWaitpointStore(wp)
	actions := &syncWaitActions{db: db}
	h.SetCrewshipActions(actions)
	lifecycle, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.SetLifecycleContext(lifecycle)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus) VALUES ('held-crew', ?, 'Crew', 'held-crew', 'free', 4096, 2.0)`, ws); err != nil {
		t.Fatal(err)
	}
	return &syncWaitRig{h: h, db: db, user: user, ws: ws, wp: wp, actions: actions}
}

func (r *syncWaitRig) seedPipeline(t *testing.T, id, slug, def string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, ?, 'hash', ?, ?, ?)`, id, r.ws, slug, slug, def, now, now, now); err != nil {
		t.Fatal(err)
	}
}

// syncWithin fails the test when f does not return in time: a synchronous call that
// holds its request open for a person's decision hangs here.
func syncWithin(t *testing.T, limit time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s did not return within %s: it holds its request open while a person decides", what, limit)
	}
}

type syncReceipt struct {
	RunID          string `json:"run_id"`
	PipelineID     string `json:"pipeline_id"`
	Status         string `json:"status"`
	WaitpointToken string `json:"waitpoint_token"`
	CurrentStep    string `json:"current_step"`
}

func (r *syncWaitRig) mustWaiting(t *testing.T, rr *httptest.ResponseRecorder) syncReceipt {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var rc syncReceipt
	if err := json.Unmarshal(rr.Body.Bytes(), &rc); err != nil {
		t.Fatal(err)
	}
	if rc.Status != "WAITING" || rc.WaitpointToken == "" || rc.RunID == "" {
		t.Fatalf("want a WAITING receipt, got %s", rr.Body.String())
	}
	return rc
}

func (r *syncWaitRig) approveAndWaitCompleted(t *testing.T, runID, token string) {
	t.Helper()
	if err := r.wp.CompleteApproval(t.Context(), r.ws, token, true, r.user, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		_ = r.db.QueryRow(`SELECT status FROM pipeline_runs WHERE id = ?`, runID).Scan(&status)
		if strings.EqualFold(status, "completed") {
			return
		}
		if strings.EqualFold(status, "failed") || strings.EqualFold(status, "cancelled") {
			t.Fatalf("run %s ended %s after the approval", runID, status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not complete after the approval", runID)
}

func TestSyncWait_ReplayReturnsWaitingReceipt(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	seedRunRow(t, rig.db, rig.ws, "pln_held", "held-each", "run_src", "completed")
	req := withWorkspaceUser(httptest.NewRequest("POST", "/replay", strings.NewReader(`{}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("runId", "run_src")
	rr := httptest.NewRecorder()
	syncWithin(t, 15*time.Second, "replay", func() { rig.h.ReplayRun(rr, req) })
	rc := rig.mustWaiting(t, rr)
	rig.approveAndWaitCompleted(t, rc.RunID, rc.WaitpointToken)
	if got := rig.actions.executed(); got != 1 {
		t.Fatalf("the approved action executed %d times, want once", got)
	}
}

func TestSyncWait_ScheduleRunNowReturnsWaitingReceipt(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.h.SetScheduleStore(pipeline.NewScheduleStore(rig.db))
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	saved, err := pipeline.NewScheduleStore(rig.db).Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: rig.ws, Name: "held", TargetPipelineID: "pln_held", CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/schedule/run", nil), rig.user, rig.ws, "OWNER")
	req.SetPathValue("scheduleId", saved.ID)
	rr := httptest.NewRecorder()
	syncWithin(t, 15*time.Second, "schedule run-now", func() { rig.h.RunSchedule(rr, req) })
	rc := rig.mustWaiting(t, rr)
	rig.approveAndWaitCompleted(t, rc.RunID, rc.WaitpointToken)
	if got := rig.actions.executed(); got != 1 {
		t.Fatalf("the approved action executed %d times, want once", got)
	}
}

// Every batch item that waits is reported as WAITING (neither success nor
// error) and the rest of the batch still runs; each approval completes its own
// run exactly once.
func TestSyncWait_BatchReportsWaitingItemsAndKeepsGoing(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run_batch", strings.NewReader(`{"items":[{"inputs":{}},{"inputs":{}}]}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("slug", "held-each")
	rr := httptest.NewRecorder()
	syncWithin(t, 20*time.Second, "batch", func() { rig.h.RunBatch(rr, req) })
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Count   int `json:"count"`
		Results []struct {
			Index          int    `json:"index"`
			RunID          string `json:"run_id"`
			Status         string `json:"status"`
			Error          string `json:"error"`
			WaitpointToken string `json:"waitpoint_token"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Count != 2 || len(out.Results) != 2 {
		t.Fatalf("batch result: %v %s", err, rr.Body.String())
	}
	for _, it := range out.Results {
		if it.Status != "WAITING" || it.WaitpointToken == "" || it.Error != "" {
			t.Fatalf("item %d = %+v, want WAITING with a token and no error", it.Index, it)
		}
	}
	for _, it := range out.Results {
		rig.approveAndWaitCompleted(t, it.RunID, it.WaitpointToken)
	}
	if got := rig.actions.executed(); got != 2 {
		t.Fatalf("approved actions executed %d times, want once per item (2)", got)
	}
}

// A held action in a CALLED routine makes the synchronous run of the parent
// return a receipt for the PARENT run, the run the caller started.
func TestSyncWait_NestedRoutineReceiptNamesTheParentRun(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_child", "held-each", syncWaitHeldDef)
	rig.seedPipeline(t, "pln_parent", "parent-call", `{"dsl_version":"1.0","name":"parent-call","steps":[{"id":"call","type":"call_pipeline","pipeline_slug":"held-each"}]}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("slug", "parent-call")
	rr := httptest.NewRecorder()
	syncWithin(t, 20*time.Second, "run of a routine that calls a held routine", func() { rig.h.Run(rr, req) })
	rc := rig.mustWaiting(t, rr)
	var pid, via string
	if err := rig.db.QueryRow(`SELECT pipeline_id, triggered_via FROM pipeline_runs WHERE id = ?`, rc.RunID).Scan(&pid, &via); err != nil {
		t.Fatal(err)
	}
	if pid != "pln_parent" || via == "call_pipeline" {
		t.Fatalf("receipt run %s belongs to pipeline %s (%s), want the parent run the caller started", rc.RunID, pid, via)
	}
	rig.approveAndWaitCompleted(t, rc.RunID, rc.WaitpointToken)
	if got := rig.actions.executed(); got != 1 {
		t.Fatalf("the approved action executed %d times, want once", got)
	}
}

// A decision that arrives before the receipt has been sent must not cancel or
// duplicate the run: either the finished result or the receipt is returned,
// and the action ran once.
func TestSyncWait_ApprovalBeforeTheReceiptIsSent(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
	exec := rig.h.newExecutor()
	input := pipeline.RunInput{PipelineID: "pln_held", WorkspaceID: rig.ws, Mode: pipeline.ModeRun,
		OnHumanWait: func(_, _, token string) {
			// Runs inside the run, before the wait is handed to the request.
			go func() { _ = rig.wp.CompleteApproval(context.Background(), rig.ws, token, true, rig.user, "") }()
		}}
	var res, receipt *pipeline.RunResult
	var err error
	syncWithin(t, 20*time.Second, "run", func() { res, receipt, err = rig.h.runSynchronously(req, exec, input) })
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case receipt != nil && receipt.Status == "WAITING", res != nil && res.Status == "COMPLETED":
	default:
		t.Fatalf("res=%+v receipt=%+v, want the finished result or a WAITING receipt", res, receipt)
	}
	deadline := time.Now().Add(15 * time.Second)
	for rig.actions.executed() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if got := rig.actions.executed(); got != 1 {
		t.Fatalf("the approved action executed %d times, want once", got)
	}
}

// A client that disconnects before anything waits still cancels the run, on
// every synchronous entry point (#5353 keeps this by design).
func TestSyncWait_DisconnectBeforeAnyWaitStillCancels(t *testing.T) {
	for _, entry := range []string{"run", "batch"} {
		t.Run(entry, func(t *testing.T) {
			h, db, user, ws := runsHandlerRig(t)
			runner := newBlockingRunner()
			h.SetRunner(runner)
			h.SetRunStore(pipeline.NewRunStore(db))
			h.SetRunRegistry(pipeline.NewRunRegistry())
			lifecycle, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.SetLifecycleContext(lifecycle)
			seedAgentRunPipeline(t, db, ws, "pln_block", "block")
			body := `{"inputs":{}}`
			if entry == "batch" {
				body = `{"items":[{"inputs":{}}]}`
			}
			req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), user, ws, "OWNER")
			req.SetPathValue("slug", "block")
			reqCtx, disconnect := context.WithCancel(req.Context())
			defer disconnect()
			req = req.WithContext(reqCtx)
			rr := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if entry == "batch" {
					h.RunBatch(rr, req)
				} else {
					h.Run(rr, req)
				}
			}()
			select {
			case <-runner.started:
			case <-time.After(5 * time.Second):
				t.Fatal("the run never started")
			}
			disconnect()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the handler kept running after the client left")
			}
			deadline := time.Now().Add(5 * time.Second)
			for runner.observedCtxErr() == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if runner.observedCtxErr() == nil {
				t.Fatal("a disconnect before any wait did not cancel the run")
			}
		})
	}
}

// The waitpoint token in a WAITING receipt is only a handle. An agent (an
// internal, crew-bound token) must not be able to approve or deny the step it
// holds, neither through the approve door nor through the public callback
// with that same token; only a person can.
func TestSyncWait_ReceiptTokenCannotBeUsedByAnAgent(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("slug", "held-each")
	rr := httptest.NewRecorder()
	syncWithin(t, 20*time.Second, "run", func() { rig.h.Run(rr, req) })
	rc := rig.mustWaiting(t, rr)

	pending := func() string {
		var st string
		_ = rig.db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, rc.WaitpointToken).Scan(&st)
		return st
	}
	for _, approved := range []bool{true, false} {
		body := `{"approved":false}`
		if approved {
			body = `{"approved":true}`
		}
		// The agent's request: no user session, a crew-bound internal token.
		ar := httptest.NewRequest("POST", "/approve", strings.NewReader(body))
		ctx := context.WithValue(ar.Context(), ctxInternalTokenCrew, "held-crew")
		ctx = context.WithValue(ctx, ctxWorkspaceID, rig.ws)
		ctx = context.WithValue(ctx, ctxRole, "OWNER")
		ar = ar.WithContext(ctx)
		ar.SetPathValue("token", rc.WaitpointToken)
		arr := httptest.NewRecorder()
		rig.h.ApproveWaitpoint(arr, ar)
		if arr.Code != http.StatusForbidden {
			t.Fatalf("agent decision (approved=%v) = %d %s, want 403", approved, arr.Code, arr.Body.String())
		}
		if got := pending(); got != "pending" {
			t.Fatalf("waitpoint is %q after an agent's attempt, want it untouched (pending)", got)
		}
	}
	// The public callback needs its own secret, not the handle.
	cr := httptest.NewRequest("POST", "/api/v1/waitpoint-tokens/x", strings.NewReader(`{"approved":true}`))
	cr.SetPathValue("token", rc.WaitpointToken)
	crr := httptest.NewRecorder()
	rig.h.CompleteWaitpointToken(crr, cr)
	if crr.Code == http.StatusOK {
		t.Fatalf("the receipt's handle completed the wait through the public callback: %s", crr.Body.String())
	}
	if got := pending(); got != "pending" {
		t.Fatalf("waitpoint is %q after the callback attempt, want pending", got)
	}
	rig.approveAndWaitCompleted(t, rc.RunID, rc.WaitpointToken)
}

// A run that keeps going after its WAITING receipt re-checks the invoking
// person when it continues: if that person left the workspace meanwhile, the
// approved action does not execute.
func TestSyncWait_ContinuingRunRechecksTheInvokingPerson(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("slug", "held-each")
	rr := httptest.NewRecorder()
	syncWithin(t, 20*time.Second, "run", func() { rig.h.Run(rr, req) })
	rc := rig.mustWaiting(t, rr)

	for _, q := range []string{
		`INSERT INTO users (id, email, full_name) VALUES ('u_second_admin', 'second@example.test', 'Second')`,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm_second_admin', '` + rig.ws + `', 'u_second_admin', 'ADMIN')`,
	} {
		if _, err := rig.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rig.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, rig.ws, rig.user); err != nil {
		t.Fatal(err)
	}
	if err := rig.wp.CompleteApproval(t.Context(), rig.ws, rc.WaitpointToken, true, "u_second_admin", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		_ = rig.db.QueryRow(`SELECT status FROM pipeline_runs WHERE id = ?`, rc.RunID).Scan(&status)
		if s := strings.ToLower(status); s == "failed" || s == "cancelled" || s == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.EqualFold(status, "completed") || rig.actions.executed() != 0 {
		t.Fatalf("run status %q, action executed %d times: a person who left the workspace still drove the action", status, rig.actions.executed())
	}
}

// A routine deleted while its run waits does not run the approved action.
func TestSyncWait_ContinuingRunStopsWhenTheRoutineIsDeleted(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("slug", "held-each")
	rr := httptest.NewRecorder()
	syncWithin(t, 20*time.Second, "run", func() { rig.h.Run(rr, req) })
	rc := rig.mustWaiting(t, rr)
	if _, err := rig.db.Exec(`UPDATE pipelines SET deleted_at = ? WHERE id = 'pln_held'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := rig.wp.CompleteApproval(t.Context(), rig.ws, rc.WaitpointToken, true, rig.user, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		_ = rig.db.QueryRow(`SELECT status FROM pipeline_runs WHERE id = ?`, rc.RunID).Scan(&status)
		if s := strings.ToLower(status); s == "failed" || s == "cancelled" || s == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.EqualFold(status, "completed") || rig.actions.executed() != 0 {
		t.Fatalf("run status %q, action executed %d times after the routine was deleted", status, rig.actions.executed())
	}
}

// A routine disabled or sent back to "proposed" while its run waits does not
// run the approved action either.
func TestSyncWait_ContinuingRunStopsWhenTheRoutineIsNoLongerRunnable(t *testing.T) {
	for _, status := range []string{"disabled", "proposed"} {
		t.Run(status, func(t *testing.T) {
			rig := newSyncWaitRig(t)
			rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
			req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
			req.SetPathValue("slug", "held-each")
			rr := httptest.NewRecorder()
			syncWithin(t, 20*time.Second, "run", func() { rig.h.Run(rr, req) })
			rc := rig.mustWaiting(t, rr)
			if _, err := rig.db.Exec(`UPDATE pipelines SET status = ? WHERE id = 'pln_held'`, status); err != nil {
				t.Fatal(err)
			}
			if err := rig.wp.CompleteApproval(t.Context(), rig.ws, rc.WaitpointToken, true, rig.user, ""); err != nil {
				t.Fatalf("approve: %v", err)
			}
			deadline := time.Now().Add(15 * time.Second)
			var st string
			for time.Now().Before(deadline) {
				_ = rig.db.QueryRow(`SELECT status FROM pipeline_runs WHERE id = ?`, rc.RunID).Scan(&st)
				if s := strings.ToLower(st); s == "failed" || s == "cancelled" || s == "completed" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if strings.EqualFold(st, "completed") || rig.actions.executed() != 0 {
				t.Fatalf("run status %q, action executed %d times for a %s routine", st, rig.actions.executed(), status)
			}
		})
	}
}

// A client that leaves AFTER one batch item started waiting: that item still
// completes with its approval, exactly once; the items not yet started are not
// started; nothing runs twice.
func TestSyncWait_BatchDisconnectAfterWaitingKeepsTheWaitingItem(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run_batch", strings.NewReader(`{"items":[{"inputs":{}},{"inputs":{}},{"inputs":{}}]}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("slug", "held-each")
	reqCtx, disconnect := context.WithCancel(req.Context())
	defer disconnect()
	req = req.WithContext(reqCtx)
	// Leave exactly when the first item has signalled that it waits for a
	// person: not earlier (the pending row is written before the signal) and
	// not by polling.
	var once sync.Once
	rig.h.syncRunHooks.afterWaitRecorded = func() { once.Do(disconnect) }
	rr := httptest.NewRecorder()
	syncWithin(t, 20*time.Second, "batch", func() { rig.h.RunBatch(rr, req) })
	var out struct {
		Results []struct {
			RunID          string `json:"run_id"`
			Status         string `json:"status"`
			Error          string `json:"error"`
			WaitpointToken string `json:"waitpoint_token"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || len(out.Results) != 3 {
		t.Fatalf("batch result: %v %s", err, rr.Body.String())
	}
	first := out.Results[0]
	if first.Status != "WAITING" || first.WaitpointToken == "" {
		t.Fatalf("first item = %+v, want WAITING", first)
	}
	// An item the client's departure raced with may already have started
	// waiting; every item that waits must survive the disconnect and finish
	// once with its approval. The others never started or were cancelled with
	// the request; none completes on its own.
	waiting := 0
	for i, it := range out.Results {
		switch {
		case it.Status == "WAITING":
			waiting++
		case strings.EqualFold(it.Status, "COMPLETED"):
			t.Fatalf("item %d = %+v completed without a decision", i, it)
		}
	}
	for _, it := range out.Results {
		if it.Status == "WAITING" {
			rig.approveAndWaitCompleted(t, it.RunID, it.WaitpointToken)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := rig.actions.executed(); got != waiting {
		t.Fatalf("approved actions executed %d times, want once per waiting item (%d)", got, waiting)
	}
}

// Both the wait signal and the client's departure are ready at the same time:
// the run that already waits must answer WAITING every time, not whichever
// select case is picked.
func TestSyncWait_SignalAndDisconnectTogetherAlwaysReceipt(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	for i := 0; i < 40; i++ {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
		req.SetPathValue("slug", "held-each")
		reqCtx, disconnect := context.WithCancel(req.Context())
		req = req.WithContext(reqCtx)
		rig.h.syncRunHooks.afterWaitRecorded = disconnect
		rr := httptest.NewRecorder()
		syncWithin(t, 20*time.Second, "run", func() { rig.h.Run(rr, req) })
		disconnect()
		rc := rig.mustWaiting(t, rr)
		rig.approveAndWaitCompleted(t, rc.RunID, rc.WaitpointToken)
	}
}

// The client leaves in the gap between the waitpoint row and the signal: that
// is "before waiting", so the run is cancelled and nothing stays actionable.
func TestSyncWait_DisconnectBetweenWaitpointAndSignalCancelsAndClosesTheCard(t *testing.T) {
	rig := newSyncWaitRig(t)
	rig.seedPipeline(t, "pln_held", "held-each", syncWaitHeldDef)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), rig.user, rig.ws, "OWNER")
	req.SetPathValue("slug", "held-each")
	reqCtx, disconnect := context.WithCancel(req.Context())
	defer disconnect()
	req = req.WithContext(reqCtx)
	rig.h.syncRunHooks.beforeWaitSignal = func(runCtx context.Context) {
		disconnect()
		select {
		case <-runCtx.Done(): // the handler decided "client left first"
		case <-time.After(10 * time.Second):
		}
	}
	rr := httptest.NewRecorder()
	syncWithin(t, 30*time.Second, "run", func() { rig.h.Run(rr, req) })
	var out struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if strings.EqualFold(out.Status, "WAITING") || strings.EqualFold(out.Status, "COMPLETED") {
		t.Fatalf("a run the client left before it waited answered %s", rr.Body.String())
	}
	var pending int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("%d waitpoints still pending: %v", pending, err)
	}
	var open int
	_ = rig.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = 'waitpoint' AND resolved_at IS NULL`).Scan(&open)
	if open != 0 {
		t.Fatalf("%d approval cards are still actionable", open)
	}
	if rig.actions.executed() != 0 {
		t.Fatal("the action ran")
	}
}
