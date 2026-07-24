package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ---------------------------------------------------------------------------
// pipelines_exec.go — ListRuns + ApproveWaitpoint + ListPendingWaitpoints.
//
// These three handlers power the routine-detail page (run history),
// the inbox approval cards, and the approval-completion endpoint.
// Run/DryRun/TestRun are already partially covered; this fills in the
// zero-coverage list/approve trio.
// ---------------------------------------------------------------------------

// stubApproverWaitpoints implements pipeline.WaitpointStore AND the
// inline `approver` interface ApproveWaitpoint type-asserts to.
type stubApproverWaitpoints struct {
	completeCalls            int
	gotWorkspaceID           string
	gotToken                 string
	gotApproved              bool
	gotDecider               string
	gotPayload               string
	completeReturnAlready    bool
	completeReturnGenericErr error
}

func (s *stubApproverWaitpoints) CreateApproval(_ context.Context, _ pipeline.WaitpointApprovalRequest) (string, error) {
	return "", nil
}
func (s *stubApproverWaitpoints) WaitFor(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// CompleteApproval matches the inline `approver` interface in
// ApproveWaitpoint. Production wiring uses *pipeline.SQLWaitpointStore.
func (s *stubApproverWaitpoints) CompleteApproval(_ context.Context, workspaceID, token string, approved bool, deciderUserID, payload string) error {
	s.completeCalls++
	s.gotWorkspaceID = workspaceID
	s.gotToken = token
	s.gotApproved = approved
	s.gotDecider = deciderUserID
	s.gotPayload = payload
	if s.completeReturnAlready {
		return &simpleErr{msg: "waitpoint: already decided or expired"}
	}
	return s.completeReturnGenericErr
}

// stubBareWaitpoints satisfies only pipeline.WaitpointStore (no
// CompleteApproval method). Used to exercise the 503 "does not support
// completion" branch in ApproveWaitpoint.
type stubBareWaitpoints struct{}

func (stubBareWaitpoints) CreateApproval(_ context.Context, _ pipeline.WaitpointApprovalRequest) (string, error) {
	return "", nil
}
func (stubBareWaitpoints) WaitFor(_ context.Context, _ string) (bool, error) { return false, nil }

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// ---- ListRuns ----

func TestListRuns_NotFound(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestListRuns_CrossWorkspace_NotFound(t *testing.T) {
	// Pipeline lives in a different workspace; the GetBySlug call must
	// 404 — pins the no-cross-workspace-leak contract.
	h, userID, wsA := newPipelineHandlerForCRUDTest(t)
	wsB := "ws-listruns-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-listruns')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedPipelineWithVersions(t, h, wsB, "pln-listruns-foreign", "foreign-slug", 1)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "foreign-slug")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace = %d, want 404", rr.Code)
	}
}

func TestListRuns_EmptyHistory_ReturnsEmptyArray(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-empty-runs", "no-runs", 1)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "no-runs")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("empty runs body = %q, want \"[]\" (UI iterates; never null)", rr.Body.String())
	}
}

func TestListRuns_DefaultFilter_OnlyRunLevelEntries(t *testing.T) {
	// Without include_steps=1, the LIKE filter is "pipeline.run.%" so
	// step-level entries must be excluded.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-runs", "runslug", 1)

	insertJournalEntry(t, h.db, wsID, "pipeline.run.started", "pln-runs", "run-1")
	insertJournalEntry(t, h.db, wsID, "pipeline.run.completed", "pln-runs", "run-1")
	insertJournalEntry(t, h.db, wsID, "pipeline.step.started", "pln-runs", "run-1")
	insertJournalEntry(t, h.db, wsID, "pipeline.step.completed", "pln-runs", "run-1")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "runslug")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("default filter returned %d rows, want 2 (only run-level entries)", len(got))
	}
	for _, row := range got {
		et, _ := row["entry_type"].(string)
		if !strings.HasPrefix(et, "pipeline.run.") {
			t.Errorf("default filter returned non-run entry: %s", et)
		}
	}
}

func TestListRuns_IncludeStepsWidensFilter(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-widen", "widensl", 1)
	insertJournalEntry(t, h.db, wsID, "pipeline.run.started", "pln-widen", "run-A")
	insertJournalEntry(t, h.db, wsID, "pipeline.step.started", "pln-widen", "run-A")

	req := httptest.NewRequest("GET", "/x?include_steps=1", nil)
	req.SetPathValue("slug", "widensl")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if len(got) != 2 {
		t.Errorf("include_steps=1 returned %d rows, want 2 (run + step)", len(got))
	}
}

// TestListRuns_IncludesSummaryGenerated_DefaultFilter pins #1403: the
// post-run outcome verdict must surface in the routine runs tab even
// without ?include_steps=1 — it's a run-level entry (like
// pipeline.run.completed), not step-level detail, so it doesn't
// belong behind the steps-widening flag.
func TestListRuns_IncludesSummaryGenerated_DefaultFilter(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-verdict", "verdictslug", 1)
	insertJournalEntry(t, h.db, wsID, "pipeline.run.started", "pln-verdict", "run-V")
	insertJournalEntry(t, h.db, wsID, "pipeline.run.completed", "pln-verdict", "run-V")
	insertJournalEntry(t, h.db, wsID, "summary.generated", "pln-verdict", "run-V")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "verdictslug")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	found := false
	for _, row := range got {
		if row["entry_type"] == "summary.generated" {
			found = true
		}
	}
	if !found {
		t.Errorf("summary.generated missing from default-filter ListRuns response: %+v", got)
	}
}

func TestListRuns_CrossPipelineExclusion(t *testing.T) {
	// Entries for a different pipeline in the same workspace must not
	// surface — the json_extract filter on pipeline_id is the gate.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-mine", "mine", 1)
	seedPipelineWithVersions(t, h, wsID, "pln-other", "other", 1)
	insertJournalEntry(t, h.db, wsID, "pipeline.run.started", "pln-mine", "run-mine-1")
	insertJournalEntry(t, h.db, wsID, "pipeline.run.started", "pln-other", "run-other-1")

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "mine")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRuns(rr, req)
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("returned %d rows, want 1 (other pipeline must be excluded)", len(got))
	}
	if got[0]["pipeline_id"] != "pln-mine" {
		t.Errorf("got pipeline_id %v, want pln-mine", got[0]["pipeline_id"])
	}
}

func TestListRuns_LimitClamping(t *testing.T) {
	// Default limit is 50; explicit out-of-range falls back to default.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-lim", "limited", 1)
	for i := 0; i < 5; i++ {
		insertJournalEntry(t, h.db, wsID, "pipeline.run.started", "pln-lim", "r"+string(rune('a'+i)))
	}

	for _, tc := range []struct {
		name, q string
		want    int
	}{
		{"default-50", "", 5}, // only 5 rows seeded
		{"limit-2", "?limit=2", 2},
		{"limit-zero-falls-back", "?limit=0", 5},
		{"limit-negative-falls-back", "?limit=-1", 5},
		{"limit-too-large-falls-back", "?limit=99999", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x"+tc.q, nil)
			req.SetPathValue("slug", "limited")
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ListRuns(rr, req)
			var got []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
			}
			if len(got) != tc.want {
				t.Errorf("%s: rows = %d, want %d", tc.name, len(got), tc.want)
			}
		})
	}
}

// insertJournalEntry seeds a journal_entries row with a payload
// containing pipeline_id + run_id so ListRuns' json_extract filter
// matches and the response can be inspected.
func insertJournalEntry(t *testing.T, db *sql.DB, wsID, entryType, pipelineID, runID string) {
	t.Helper()
	payload := `{"pipeline_id":"` + pipelineID + `","run_id":"` + runID + `"}`
	if _, err := db.Exec(`INSERT INTO journal_entries
		(id, workspace_id, entry_type, severity, actor_type, summary, payload)
		VALUES (?, ?, ?, 'info', 'orchestrator', ?, ?)`,
		"je-"+pipelineID+"-"+runID+"-"+entryType, wsID, entryType, entryType, payload); err != nil {
		t.Fatalf("insert journal_entry: %v", err)
	}
}

// ---- ApproveWaitpoint ----

func TestApproveWaitpoint_NoWaitpointStore_503(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	// Don't call SetWaitpointStore.
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true}`))
	req.SetPathValue("token", "tok-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (waitpoint store not wired)", rr.Code)
	}
}

func TestApproveWaitpoint_MissingToken_400(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetWaitpointStore(&stubApproverWaitpoints{})
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true}`))
	// No SetPathValue → empty token
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestApproveWaitpoint_InvalidJSON_400(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetWaitpointStore(&stubApproverWaitpoints{})

	body := strings.NewReader("not-json")
	req := httptest.NewRequest("POST", "/x", body)
	req.ContentLength = int64(body.Len())
	req.SetPathValue("token", "tok-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestApproveWaitpoint_StoreDoesNotSupportCompletion_503(t *testing.T) {
	// stubBareWaitpoints satisfies WaitpointStore but NOT the inline
	// approver interface. ApproveWaitpoint must surface 503 rather
	// than nil-panic on the failed type-assertion.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetWaitpointStore(stubBareWaitpoints{})
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"approved":true}`))
	req.SetPathValue("token", "tok-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (interface missing CompleteApproval)", rr.Code)
	}
}

func TestApproveWaitpoint_HappyPath_ForwardsApprovedAndDeciderToStore(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	stub := &stubApproverWaitpoints{}
	h.SetWaitpointStore(stub)

	body := strings.NewReader(`{"approved":true,"comment":"LGTM"}`)
	req := httptest.NewRequest("POST", "/x", body)
	req.ContentLength = int64(body.Len())
	req.SetPathValue("token", "tok-approve-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if stub.completeCalls != 1 {
		t.Errorf("CompleteApproval called %d times, want 1", stub.completeCalls)
	}
	if stub.gotWorkspaceID != wsID {
		t.Errorf("workspaceID = %q, want %q (#1415: must thread the caller's workspace through)", stub.gotWorkspaceID, wsID)
	}
	if stub.gotToken != "tok-approve-1" {
		t.Errorf("token = %q", stub.gotToken)
	}
	if !stub.gotApproved {
		t.Error("approved = false, want true")
	}
	if stub.gotDecider != userID {
		t.Errorf("decider = %q, want %q (extracted from JWT user context)", stub.gotDecider, userID)
	}
	if stub.gotPayload != "LGTM" {
		t.Errorf("payload = %q, want \"LGTM\" (body.Comment threads through)", stub.gotPayload)
	}
}

func TestApproveWaitpoint_AlreadyDecided_409(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	stub := &stubApproverWaitpoints{completeReturnAlready: true}
	h.SetWaitpointStore(stub)

	body := strings.NewReader(`{"approved":false}`)
	req := httptest.NewRequest("POST", "/x", body)
	req.ContentLength = int64(body.Len())
	req.SetPathValue("token", "tok-dup")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already decided)", rr.Code)
	}
}

// TestApproveWaitpoint_CrossTenant_409 is the #1415 regression at the HTTP
// boundary, complementing the store-level TestWaitpointStore_CrossTenantApprovalRejected.
// A MANAGER of workspace B who holds (or guesses) workspace A's pending
// approval token must NOT be able to release A's gated run. The handler threads
// WorkspaceIDFromContext into CompleteApproval, whose UPDATE is scoped by
// workspace_id, so a cross-tenant POST matches zero rows → ErrAlreadyDecided →
// 409, and A's waitpoint stays pending for its real owner to decide. Uses a
// real SQLWaitpointStore (not the stub) so the workspace_id filter actually runs.
func TestApproveWaitpoint_CrossTenant_409(t *testing.T) {
	h, _, wsA := newPipelineHandlerForCRUDTest(t)
	store := pipeline.NewSQLWaitpointStore(h.db)
	t.Cleanup(store.Close)
	h.SetWaitpointStore(store)

	// Owner of workspace A parks a run on an approval waitpoint.
	token, err := store.CreateApproval(context.Background(), pipeline.WaitpointApprovalRequest{
		WorkspaceID:   wsA,
		PipelineRunID: "run_A",
		StepID:        "gate",
		Prompt:        "ok?",
		TimeoutSec:    30,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	// Attacker: MANAGER of a DIFFERENT workspace, holding A's token.
	body := strings.NewReader(`{"approved":true,"comment":"pwned"}`)
	req := httptest.NewRequest("POST", "/x", body)
	req.ContentLength = int64(body.Len())
	req.SetPathValue("token", token)
	req = withWorkspaceUser(req, "u_attacker", "ws_B_other", "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("cross-tenant approve status = %d body=%s, want 409 (workspace-scoped UPDATE matches 0 rows)", rr.Code, rr.Body.String())
	}

	// The waitpoint must still be pending and unattributed to the attacker.
	var status, decidedBy string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(decided_by_user_id,'') FROM pipeline_waitpoints WHERE token = ?`, token).
		Scan(&status, &decidedBy); err != nil {
		t.Fatalf("read waitpoint: %v", err)
	}
	if status != "pending" {
		t.Errorf("waitpoint status = %q after cross-tenant approve, want still pending", status)
	}
	if decidedBy != "" {
		t.Errorf("decided_by = %q, want empty (attacker must not be recorded)", decidedBy)
	}
}

func TestApproveWaitpoint_NoBody_DefaultsToApprovedFalse(t *testing.T) {
	// Source: "if r.ContentLength > 0" gates the JSON decode; with
	// ContentLength 0 the body struct stays zero-value. The endpoint
	// then completes with approved=false — a defensive default for
	// callers that POST without a body. Verify the path doesn't crash
	// and CompleteApproval is invoked.
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	stub := &stubApproverWaitpoints{}
	h.SetWaitpointStore(stub)

	req := httptest.NewRequest("POST", "/x", nil) // no body, ContentLength=0
	req.SetPathValue("token", "tok-empty")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if stub.completeCalls != 1 || stub.gotApproved {
		t.Errorf("calls=%d approved=%v, want 1 + false", stub.completeCalls, stub.gotApproved)
	}
}

// ---- ListPendingWaitpoints ----

func TestListPendingWaitpoints_EmptyWorkspace_ReturnsEmptyArray(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListPendingWaitpoints(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("empty body = %q, want \"[]\"", rr.Body.String())
	}
}

func TestListPendingWaitpoints_FiltersByStatusPendingAndWorkspace(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	wsB := "ws-wp-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-wp')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// pending — should appear
	insertWaitpointRow(t, h.db, "tok-pending-1", wsID, "approval", "pending", "approve me?", now)
	// approved — should NOT appear (filter)
	insertWaitpointRow(t, h.db, "tok-decided", wsID, "approval", "approved", "done", now)
	// foreign workspace pending — should NOT appear
	insertWaitpointRow(t, h.db, "tok-foreign", wsB, "approval", "pending", "not mine", now)
	// second pending in our workspace — should appear
	insertWaitpointRow(t, h.db, "tok-pending-2", wsID, "event", "pending", "event waiting", now)

	req := httptest.NewRequest("GET", "/x", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListPendingWaitpoints(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (pending only, own workspace only)", len(got))
	}
	for _, row := range got {
		tok, _ := row["token"].(string)
		if tok == "tok-decided" {
			t.Errorf("decided waitpoint leaked: %+v", row)
		}
		if tok == "tok-foreign" {
			t.Errorf("foreign-workspace waitpoint leaked: %+v", row)
		}
	}
}

// TestApproveWaitpoint_CrossWorkspace_CannotApproveForeignWaitpoint pins
// #1415: a MANAGER/OWNER in workspace A must not be able to approve or
// deny a pending waitpoint token that belongs to workspace B, even
// though the URL role-gate on {workspaceId} passes (the token happens
// to leak or get guessed). This exercises the REAL SQLWaitpointStore
// (not the stub) so the workspace-scoped SQL predicate in
// pipeline.CompleteApproval is what's actually under test — mirrors
// SignalRun's rec.WorkspaceID != workspaceID guard.
func TestApproveWaitpoint_CrossWorkspace_CannotApproveForeignWaitpoint(t *testing.T) {
	h, userID, wsA := newPipelineHandlerForCRUDTest(t)
	wsB := "ws-waitpoint-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-waitpoint')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}

	store := pipeline.NewSQLWaitpointStore(h.db)
	t.Cleanup(store.Close)
	h.SetWaitpointStore(store)

	token, err := store.CreateApproval(context.Background(), pipeline.WaitpointApprovalRequest{
		WorkspaceID:   wsB,
		PipelineRunID: "run-foreign",
		StepID:        "gate",
		Prompt:        "approve me?",
		TimeoutSec:    3600,
	})
	if err != nil {
		t.Fatalf("seed foreign waitpoint: %v", err)
	}

	// Attacker: an authenticated MANAGER of workspace A, hitting the
	// workspace-A-scoped route with workspace B's token.
	body := strings.NewReader(`{"approved":true,"comment":"pwned"}`)
	req := httptest.NewRequest("POST", "/x", body)
	req.ContentLength = int64(body.Len())
	req.SetPathValue("token", token)
	req = withWorkspaceUser(req, userID, wsA, "MANAGER")
	rr := httptest.NewRecorder()
	h.ApproveWaitpoint(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("cross-workspace approve succeeded: status=%d body=%s", rr.Code, rr.Body.String())
	}

	// The waitpoint must still be pending — the cross-tenant decision
	// must not have landed, regardless of which status code the
	// handler chose to surface.
	var status string
	if err := h.db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&status); err != nil {
		t.Fatalf("query waitpoint status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("waitpoint status = %q, want pending (cross-workspace approval must not have applied)", status)
	}

	// The legitimate owner (workspace B) can still approve it.
	body2 := strings.NewReader(`{"approved":true}`)
	req2 := httptest.NewRequest("POST", "/x", body2)
	req2.ContentLength = int64(body2.Len())
	req2.SetPathValue("token", token)
	req2 = withWorkspaceUser(req2, userID, wsB, "MANAGER")
	rr2 := httptest.NewRecorder()
	h.ApproveWaitpoint(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("legitimate owner approve failed: status=%d body=%s", rr2.Code, rr2.Body.String())
	}
	if err := h.db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&status); err != nil {
		t.Fatalf("query waitpoint status after legitimate approve: %v", err)
	}
	if status != "approved" {
		t.Fatalf("waitpoint status = %q, want approved after legitimate owner approves", status)
	}
}

// insertWaitpointRow seeds a pipeline_waitpoints row.
func insertWaitpointRow(t *testing.T, db *sql.DB, token, wsID, kind, status, prompt, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO pipeline_waitpoints
		(token, workspace_id, pipeline_run_id, step_id, kind, prompt, status, timeout_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token, wsID, "run-"+token, "step-"+token, kind, prompt, status, createdAt, createdAt); err != nil {
		t.Fatalf("insert pipeline_waitpoints: %v", err)
	}
}
