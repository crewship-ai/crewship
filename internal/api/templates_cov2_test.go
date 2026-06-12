package api

// Final cov2 top-ups for templates.go, crew_integrations.go (ListCrew
// query error), onboarding.go (empty-provider credential default +
// double-failure rollback), and issue_handler_workflow.go (Review /
// Start / Stop DB-failure forks). All tests are prefixed TestCov2X.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- templates.go ---

func TestCov2XTemplatesList_ScanError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())
	if _, err := db.Exec(`DROP TABLE workflow_templates`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE workflow_templates (
		id TEXT PRIMARY KEY, workspace_id TEXT, name TEXT, description TEXT,
		template_json TEXT, icon TEXT, color TEXT, is_builtin INTEGER DEFAULT 0,
		created_at TEXT DEFAULT (datetime('now')), updated_at TEXT DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_templates (id, workspace_id, name, template_json)
		VALUES ('wt-null', ?, NULL, '{}')`, wsID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (NULL name scan), body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2XTemplatesUpdateDelete_ExecBlocked500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewTemplateHandler(db, newTestLogger())
	if _, err := db.Exec(`INSERT INTO workflow_templates (id, workspace_id, name, template_json, is_builtin)
		VALUES ('wt-cust', ?, 'Custom', '{}', 0)`, wsID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER cov2x_wt_upd BEFORE UPDATE ON workflow_templates
		BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER cov2x_wt_del BEFORE DELETE ON workflow_templates
		BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}

	mk := func(method, body string) *http.Request {
		req := httptest.NewRequest(method, "/x", strings.NewReader(body))
		req.SetPathValue("templateId", "wt-cust")
		return req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	}

	rec := httptest.NewRecorder()
	h.Update(rec, mk("PATCH", `{"name":"new"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("update status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.Delete(rec, mk("DELETE", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

// --- crew_integrations.go: ListCrewIntegrations main query error ---

func TestCov2XListCrewIntegrations_QueryError500(t *testing.T) {
	h, db, wsID, crewID := cov2CIRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE crew_mcp_servers`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ListCrewIntegrations(rec, cov2CIListCrewReq(wsID, crewID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

// --- onboarding.go: empty provider defaults to ANTHROPIC on template path ---

func TestCov2XOnbTemplate_EmptyProviderDefaultsAnthropic(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewOnboardingHandler(db, nil, testLogger())

	// llm_provider omitted entirely → resolveLLMProvider("") yields the
	// ANTHROPIC default; validate passes via the (test-skipped) probe.
	body := `{"crew_template_slug":"software-development","credential_value":"sk-ant-oat01-shape-ok"}`
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, body))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	var provider string
	if err := db.QueryRow(`SELECT provider FROM credentials WHERE workspace_id = ?`, wsID).Scan(&provider); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if provider != "ANTHROPIC" {
		t.Errorf("provider = %q, want ANTHROPIC default", provider)
	}
}

// --- onboarding.go: credential-store failure AND rollback failure ---

func TestCov2XOnbTemplate_CredentialAndRollbackBothFail500(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	cov2OnbAbortTrigger(t, db, "cov2x_onb_cred", "INSERT ON credentials")
	if _, err := db.Exec(`CREATE TRIGGER cov2x_onb_rb BEFORE UPDATE ON users
		WHEN NEW.onboarding_completed = 0
		BEGIN SELECT RAISE(ABORT, 'rollback blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	h := NewOnboardingHandler(db, nil, testLogger())

	body := `{"crew_template_slug":"software-development","llm_provider":"OLLAMA","credential_value":"tok"}`
	w := httptest.NewRecorder()
	h.Setup(w, cov2OnbSetupReq(userID, body))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", w.Code, w.Body.String())
	}
	// Rollback was blocked → flag stays claimed.
	if !cov2OnbCompleted(t, db, userID) {
		t.Error("expected onboarding_completed to remain 1 (rollback blocked)")
	}
}

// --- issue_handler_workflow.go ---

func TestCov2XIssueReview_ApproveUpdateBlocked500(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-90", "REVIEW")
	if _, err := h.db.Exec(`CREATE TRIGGER cov2x_iw_appr BEFORE UPDATE ON missions
		BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Review(rec, covIWReq(userID, wsID, "OWNER", "POST", `{"action":"approve"}`, crewID, "ENG-90"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2XIssueReview_RequestChangesUpdateBlocked500(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-91", "REVIEW")
	if _, err := h.db.Exec(`CREATE TRIGGER cov2x_iw_rc BEFORE UPDATE ON missions
		BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Review(rec, covIWReq(userID, wsID, "OWNER", "POST", `{"action":"request_changes"}`, crewID, "ENG-91"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2XIssueStart_CASBranches(t *testing.T) {
	t.Run("update blocked 500", func(t *testing.T) {
		h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
		missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-92", "TODO")
		if _, err := h.db.Exec(`UPDATE missions SET assignee_id = ?, assignee_type = 'agent' WHERE id = ?`, workerID, missionID); err != nil {
			t.Fatalf("assign: %v", err)
		}
		if _, err := h.db.Exec(`CREATE TRIGGER cov2x_iw_start BEFORE UPDATE ON missions
			BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
			t.Fatalf("trigger: %v", err)
		}
		rec := httptest.NewRecorder()
		h.Start(rec, covIWReq(userID, wsID, "OWNER", "POST", `{}`, crewID, "ENG-92"))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("race lost 409", func(t *testing.T) {
		h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
		missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-93", "TODO")
		if _, err := h.db.Exec(`UPDATE missions SET assignee_id = ?, assignee_type = 'agent' WHERE id = ?`, workerID, missionID); err != nil {
			t.Fatalf("assign: %v", err)
		}
		if _, err := h.db.Exec(`CREATE TRIGGER cov2x_iw_race BEFORE UPDATE ON missions
			BEGIN SELECT RAISE(IGNORE); END`); err != nil {
			t.Fatalf("trigger: %v", err)
		}
		rec := httptest.NewRecorder()
		h.Start(rec, covIWReq(userID, wsID, "OWNER", "POST", `{}`, crewID, "ENG-93"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (CAS lost), body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestCov2XIssueStop_UpdateBlocked500(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-94", "IN_PROGRESS")
	if _, err := h.db.Exec(`CREATE TRIGGER cov2x_iw_stop BEFORE UPDATE ON missions
		BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Stop(rec, covIWReq(userID, wsID, "OWNER", "POST", `{}`, crewID, "ENG-94"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
}
