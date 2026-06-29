package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- helpers --------------------------------------------------------------

// safeRoutineDef is a routine with only agent_run steps + a satisfiable (none)
// integration set → SAFE (status should be 'active').
func safeRoutineDef() string {
	return `{"dsl_version":"1.0","name":"safe-routine","steps":[` +
		`{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`
}

// httpRoutineDef carries an http step → RISKY.
func httpRoutineDef() string {
	return `{"dsl_version":"1.0","name":"risky-routine","steps":[` +
		`{"id":"h","type":"http","http":{"method":"GET","url":"https://api.example.com/x"}}]}`
}

func internalSaveBody(t *testing.T, wsID, slug, crewID, def string) string {
	t.Helper()
	fresh := time.Now().UTC().Format(time.RFC3339)
	return `{"workspace_id":"` + wsID + `","slug":"` + slug + `","name":"` + slug + `","author_crew_id":"` + crewID + `",` +
		`"last_test_run_passed":true,"last_test_run_at":"` + fresh + `","definition":` + def + `}`
}

func doInternalSave(t *testing.T, h *PipelineHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	return rr
}

func routineStatus(t *testing.T, h *PipelineHandler, wsID, slug string) string {
	t.Helper()
	var status string
	if err := h.db.QueryRow(`SELECT status FROM pipelines WHERE workspace_id = ? AND slug = ?`, wsID, slug).Scan(&status); err != nil {
		t.Fatalf("read status for %s: %v", slug, err)
	}
	return status
}

func inboxCountForRoutine(t *testing.T, h *PipelineHandler, wsID, slug string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND source_id = ? AND kind = 'escalation'`,
		wsID, routineProposalInboxSource(wsID, slug)).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return n
}

func inboxStateForRoutine(t *testing.T, h *PipelineHandler, wsID, slug string) string {
	t.Helper()
	var state string
	if err := h.db.QueryRow(
		`SELECT state FROM inbox_items WHERE workspace_id = ? AND source_id = ?`,
		wsID, routineProposalInboxSource(wsID, slug)).Scan(&state); err != nil {
		t.Fatalf("read inbox state: %v", err)
	}
	return state
}

// --- save classification --------------------------------------------------

func TestGovernance_SafeSave_Active_NoInbox(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_safe", wsID, "Eng", "eng")
	_ = seedAgentRow(t, h.db, "ag_safe", wsID, crewID, "Eva", "eva", "LEAD")

	rr := doInternalSave(t, h, internalSaveBody(t, wsID, "safe-r", crewID, safeRoutineDef()))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["status"] != "active" {
		t.Errorf("response status=%v want active", resp["status"])
	}
	if got := routineStatus(t, h, wsID, "safe-r"); got != "active" {
		t.Errorf("db status=%q want active", got)
	}
	if n := inboxCountForRoutine(t, h, wsID, "safe-r"); n != 0 {
		t.Errorf("inbox rows=%d want 0 for a safe routine", n)
	}
}

func TestGovernance_RiskyHTTPSave_Proposed_WithInbox_NotRunnable(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_risk", wsID, "Eng", "eng")
	_ = seedAgentRow(t, h.db, "ag_risk", wsID, crewID, "Eva", "eva", "LEAD")

	rr := doInternalSave(t, h, internalSaveBody(t, wsID, "risky-r", crewID, httpRoutineDef()))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["status"] != "proposed" {
		t.Errorf("response status=%v want proposed", resp["status"])
	}
	if got := routineStatus(t, h, wsID, "risky-r"); got != "proposed" {
		t.Errorf("db status=%q want proposed", got)
	}
	if n := inboxCountForRoutine(t, h, wsID, "risky-r"); n != 1 {
		t.Fatalf("inbox rows=%d want 1", n)
	}
	// Proposed routine must NOT be runnable.
	runRR := httptest.NewRecorder()
	h.Run(runRR, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "risky-r"))
	if runRR.Code != http.StatusConflict {
		t.Fatalf("run status=%d want 409 for proposed; body=%s", runRR.Code, runRR.Body.String())
	}
	if !strings.Contains(runRR.Body.String(), "awaiting approval") {
		t.Errorf("run body=%q want 'awaiting approval'", runRR.Body.String())
	}
}

func TestGovernance_UnmetIntegrationSave_Proposed(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_unmet", wsID, "Eng", "eng")
	_ = seedAgentRow(t, h.db, "ag_unmet", wsID, crewID, "Eva", "eva", "LEAD")
	// Declares slack but crew connected nothing → unmet → risky.
	def := `{"dsl_version":"1.0","name":"needs-slack","integrations_required":["slack"],"steps":[` +
		`{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`
	rr := doInternalSave(t, h, internalSaveBody(t, wsID, "needs-slack", crewID, def))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	if got := routineStatus(t, h, wsID, "needs-slack"); got != "proposed" {
		t.Errorf("db status=%q want proposed (unmet integration)", got)
	}
}

func TestGovernance_MetIntegrationSave_Active(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_met", wsID, "Eng", "eng")
	agentID := seedAgentRow(t, h.db, "ag_met", wsID, crewID, "Eva", "eva", "LEAD")
	seedComposioServer(t, h.db, wsID, agentID, "slack")
	def := `{"dsl_version":"1.0","name":"has-slack","integrations_required":["slack"],"steps":[` +
		`{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`
	rr := doInternalSave(t, h, internalSaveBody(t, wsID, "has-slack", crewID, def))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	if got := routineStatus(t, h, wsID, "has-slack"); got != "active" {
		t.Errorf("db status=%q want active (integration satisfied)", got)
	}
}

// --- approve / reject -----------------------------------------------------

func TestGovernance_Approve_ActivatesAndResolvesInbox(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_ap", wsID, "Eng", "eng")
	_ = seedAgentRow(t, h.db, "ag_ap", wsID, crewID, "Eva", "eva", "LEAD")
	if rr := doInternalSave(t, h, internalSaveBody(t, wsID, "ap-r", crewID, httpRoutineDef())); rr.Code != http.StatusCreated {
		t.Fatalf("save status=%d; body=%s", rr.Code, rr.Body.String())
	}

	// Approve as OWNER.
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), userID, wsID, "OWNER")
	req.SetPathValue("slug", "ap-r")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("approve status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := routineStatus(t, h, wsID, "ap-r"); got != "active" {
		t.Errorf("db status=%q want active after approve", got)
	}
	if st := inboxStateForRoutine(t, h, wsID, "ap-r"); st != "resolved" {
		t.Errorf("inbox state=%q want resolved", st)
	}
	// Now runnable.
	runRR := httptest.NewRecorder()
	h.Run(runRR, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "ap-r"))
	if runRR.Code != http.StatusOK {
		t.Fatalf("run after approve status=%d want 200; body=%s", runRR.Code, runRR.Body.String())
	}
}

func TestGovernance_Approve_ForbiddenForViewer(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_fv", wsID, "Eng", "eng")
	_ = seedAgentRow(t, h.db, "ag_fv", wsID, crewID, "Eva", "eva", "LEAD")
	if rr := doInternalSave(t, h, internalSaveBody(t, wsID, "fv-r", crewID, httpRoutineDef())); rr.Code != http.StatusCreated {
		t.Fatalf("save status=%d", rr.Code)
	}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), userID, wsID, "VIEWER")
	req.SetPathValue("slug", "fv-r")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("approve as VIEWER status=%d want 403", rr.Code)
	}
}

func TestGovernance_Reject_RemovesRoutine(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	crewID := seedCrewRow(t, h.db, "crew_rj", wsID, "Eng", "eng")
	_ = seedAgentRow(t, h.db, "ag_rj", wsID, crewID, "Eva", "eva", "LEAD")
	if rr := doInternalSave(t, h, internalSaveBody(t, wsID, "rj-r", crewID, httpRoutineDef())); rr.Code != http.StatusCreated {
		t.Fatalf("save status=%d", rr.Code)
	}
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), userID, wsID, "OWNER")
	req.SetPathValue("slug", "rj-r")
	rr := httptest.NewRecorder()
	h.Reject(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reject status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	// Soft-deleted → GetBySlug returns not found via the Get handler.
	var deletedAt *string
	if err := h.db.QueryRow(`SELECT deleted_at FROM pipelines WHERE workspace_id = ? AND slug = ?`, wsID, "rj-r").Scan(&deletedAt); err != nil {
		t.Fatalf("read deleted_at: %v", err)
	}
	if deletedAt == nil {
		t.Errorf("routine not soft-deleted after reject")
	}
	if st := inboxStateForRoutine(t, h, wsID, "rj-r"); st != "resolved" {
		t.Errorf("inbox state=%q want resolved after reject", st)
	}
}

// --- disable / enable -----------------------------------------------------

func TestGovernance_Disable_BlocksRun_Enable_Restores(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "crew_de", wsID, "Eng", "eng")
	_ = seedAgentRow(t, h.db, "ag_de", wsID, crewID, "Eva", "eva", "LEAD")
	// Safe routine → active.
	if rr := doInternalSave(t, h, internalSaveBody(t, wsID, "de-r", crewID, safeRoutineDef())); rr.Code != http.StatusCreated {
		t.Fatalf("save status=%d; body=%s", rr.Code, rr.Body.String())
	}

	// Disable (OWNER).
	dreq := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), userID, wsID, "OWNER")
	dreq.SetPathValue("slug", "de-r")
	drr := httptest.NewRecorder()
	h.Disable(drr, dreq)
	if drr.Code != http.StatusOK {
		t.Fatalf("disable status=%d want 200; body=%s", drr.Code, drr.Body.String())
	}
	if got := routineStatus(t, h, wsID, "de-r"); got != "disabled" {
		t.Errorf("status=%q want disabled", got)
	}
	// Disabled → run refused 409.
	runRR := httptest.NewRecorder()
	h.Run(runRR, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "de-r"))
	if runRR.Code != http.StatusConflict || !strings.Contains(runRR.Body.String(), "disabled") {
		t.Fatalf("run while disabled status=%d body=%s want 409 'disabled'", runRR.Code, runRR.Body.String())
	}

	// Disable forbidden for MANAGER (needs manage tier).
	mreq := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), userID, wsID, "MANAGER")
	mreq.SetPathValue("slug", "de-r")
	mrr := httptest.NewRecorder()
	h.Disable(mrr, mreq)
	if mrr.Code != http.StatusForbidden {
		t.Errorf("disable as MANAGER status=%d want 403", mrr.Code)
	}

	// Enable (OWNER) → active + runnable.
	ereq := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), userID, wsID, "OWNER")
	ereq.SetPathValue("slug", "de-r")
	err := httptest.NewRecorder()
	h.Enable(err, ereq)
	if err.Code != http.StatusOK {
		t.Fatalf("enable status=%d want 200; body=%s", err.Code, err.Body.String())
	}
	if got := routineStatus(t, h, wsID, "de-r"); got != "active" {
		t.Errorf("status=%q want active after enable", got)
	}
	runRR2 := httptest.NewRecorder()
	h.Run(runRR2, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "de-r"))
	if runRR2.Code != http.StatusOK {
		t.Fatalf("run after enable status=%d want 200; body=%s", runRR2.Code, runRR2.Body.String())
	}
}
