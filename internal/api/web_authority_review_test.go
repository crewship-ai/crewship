package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestScopedIssueCRUDDoesNotCaptureExecutionGrant(t *testing.T) {
	h, user, workspace, crew, _, worker := newTestIssueHandler(t)
	scoped := func(method, body, ident string) *http.Request {
		req := covIWReq(user, workspace, "OWNER", method, body, crew, ident)
		return withTokenScopes(req, "crews:write")
	}
	created := httptest.NewRecorder()
	h.Create(created, scoped("POST", `{"title":"Scoped issue","assignee_type":"agent","assignee_id":"`+worker+`"}`, ""))
	if created.Code != 201 {
		t.Fatalf("scoped create %d %s", created.Code, created.Body.String())
	}
	var issue issueResponse
	mustUnmarshal(t, created, &issue)
	var origin string
	if err := h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM missions WHERE id=?`, issue.ID).Scan(&origin); err != nil || origin != "" {
		t.Fatalf("scoped create minted grant %s %v", origin, err)
	}
	comment := httptest.NewRecorder()
	h.CreateComment(comment, scoped("POST", `{"body":"Plain scoped comment"}`, *issue.Identifier))
	if comment.Code != 201 {
		t.Fatalf("scoped comment %d %s", comment.Code, comment.Body.String())
	}
	var grants int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_origins`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("scoped CRUD minted %d grants %v", grants, err)
	}
	started := httptest.NewRecorder()
	h.Start(started, scoped("POST", `{}`, *issue.Identifier))
	if started.Code != 403 {
		t.Fatalf("scoped execution admitted %d %s", started.Code, started.Body.String())
	}
	var revision int
	if err := h.db.QueryRow(`SELECT revision FROM issue_work WHERE mission_id=?`, issue.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	handoff := scoped("POST", "", *issue.Identifier)
	handoff.Body = io.NopCloser(jsonBody(map[string]any{"operation_id": "scoped-handoff", "revision": revision, "action": "handoff_agent", "target_id": worker, "note": "Delegate work"}))
	transferred := httptest.NewRecorder()
	h.Work(transferred, handoff)
	if transferred.Code != 403 {
		t.Fatalf("scoped handoff admitted %d %s", transferred.Code, transferred.Body.String())
	}
	var after int
	if err := h.db.QueryRow(`SELECT revision FROM issue_work WHERE mission_id=?`, issue.ID).Scan(&after); err != nil || after != revision {
		t.Fatalf("denied handoff mutated work revision%d→%d %v", revision, after, err)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, issue.ID).Scan(&status); err != nil || status != "BACKLOG" {
		t.Fatalf("denied start mutated issue %s %v", status, err)
	}
}

func TestScopedRecurringCRUDClearsStandingGrantAndCanDisable(t *testing.T) {
	h, user, workspace, crew := newRecurringHandler(t)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"crew_id":"`+crew+`","title":"Standing schedule","cron_expression":"0 9 * * *"}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
	created := httptest.NewRecorder()
	h.Create(created, req)
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var template recurringIssueResponse
	mustUnmarshal(t, created, &template)
	var oldOrigin string
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM recurring_issues WHERE id=?`, template.ID).Scan(&oldOrigin); err != nil || oldOrigin == "" {
		t.Fatalf("standing grant %s %v", oldOrigin, err)
	}
	disable := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"enabled":false}`))
	disable.SetPathValue("recurringId", template.ID)
	disable = withTokenScopes(disable.WithContext(withWorkspace(withUser(disable.Context(), &AuthUser{ID: user}), workspace, "OWNER")), "workspace:admin")
	updated := httptest.NewRecorder()
	h.Update(updated, disable)
	if updated.Code != 200 {
		t.Fatalf("scoped disable %d %s", updated.Code, updated.Body.String())
	}
	var enabled bool
	var origin string
	if err := h.db.QueryRow(`SELECT enabled,COALESCE(chat_origin_id,'') FROM recurring_issues WHERE id=?`, template.ID).Scan(&enabled, &origin); err != nil || enabled || origin != "" {
		t.Fatalf("scoped edit retained grant %v %s %v", enabled, origin, err)
	}
	scopedCreate := withTokenScopes(req, "crews:write")
	scopedCreate.Body = io.NopCloser(strings.NewReader(`{"crew_id":"` + crew + `","title":"Scoped schedule","cron_expression":"0 9 * * *"}`))
	rr := httptest.NewRecorder()
	h.Create(rr, scopedCreate)
	if rr.Code != 201 {
		t.Fatalf("scoped template create %d %s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_origins`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("scoped template minted grants %d %v", count, err)
	}
}

func TestRecurringPermanentAuthorityFailureLeavesGlobalDueQueue(t *testing.T) {
	h, user, workspace, crew := newRecurringHandler(t)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"crew_id":"`+crew+`","title":"Revoked schedule","cron_expression":"0 9 * * *"}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}
	var template recurringIssueResponse
	mustUnmarshal(t, rr, &template)
	execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-01T00:00:00Z' WHERE id=?`, template.ID)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	dispatcher := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	dispatcher.tick(t.Context())
	var enabled bool
	var next string
	if err := h.db.QueryRow(`SELECT enabled,next_run FROM recurring_issues WHERE id=?`, template.ID).Scan(&enabled, &next); err != nil || enabled || next <= "2020-01-01T00:00:00Z" {
		t.Fatalf("stale authority poisons due queue %v %s %v", enabled, next, err)
	}
	var notifications int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id=?`, "recurring-authority:"+template.ID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("missing pause notice %d %v", notifications, err)
	}
	dispatcher.tick(t.Context())
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id=?`, "recurring-authority:"+template.ID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("duplicate pause notice %d %v", notifications, err)
	}
}

func TestRecurringIssuedGrantSurvivesTemplateDeletion(t *testing.T) {
	h, user, workspace, crew := newRecurringHandler(t)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"crew_id":"`+crew+`","title":"Issued work","cron_expression":"0 9 * * *"}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}
	var template recurringIssueResponse
	mustUnmarshal(t, rr, &template)
	execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-01T00:00:00Z' WHERE id=?`, template.ID)
	NewRecurringIssueDispatcher(h.db, nil, newTestLogger()).tick(t.Context())
	var issue, origin string
	if err := h.db.QueryRow(`SELECT id,chat_origin_id FROM missions WHERE workspace_id=?`, workspace).Scan(&issue, &origin); err != nil {
		t.Fatal(err)
	}
	removed := httptest.NewRequest("DELETE", "/", nil)
	removed.SetPathValue("recurringId", template.ID)
	removed = removed.WithContext(withWorkspace(withUser(removed.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
	deleted := httptest.NewRecorder()
	h.Delete(deleted, removed)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete %d %s", deleted.Code, deleted.Body.String())
	}
	proof, err := runauthority.ValidateOriginReceipt(t.Context(), h.db, origin, workspace)
	if err != nil || proof.HumanAuthority.Kind != "web_issue" || proof.HumanAuthority.ResourceID != issue {
		t.Fatalf("issued work revoked by template deletion %+v %v", proof, err)
	}
}

func TestInvalidRecurringBatchDoesNotStarveHealthyWorkspaceSchedule(t *testing.T) {
	h, user, workspace, crew := newRecurringHandler(t)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"crew_id":"`+crew+`","title":"Healthy schedule","cron_expression":"0 9 * * *"}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 201 {
		t.Fatalf("healthy template %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, h.db, `UPDATE recurring_issues SET next_run='2020-01-02T00:00:00Z' WHERE title='Healthy schedule'`)
	execOrFatal(t, h.db, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('fork-invalid-origin',?,'fork-unverified','legacy','legacy','{}',datetime('now'))`, workspace)
	execOrFatal(t, h.db, `WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100) INSERT INTO recurring_issues(id,workspace_id,crew_id,title,cron_expression,enabled,next_run,created_by,chat_origin_id) SELECT 'invalid-due-'||x,?,?,'Invalid fork '||x,'0 9 * * *',1,'2020-01-01T00:00:00Z',?,'fork-invalid-origin' FROM n`, workspace, crew, user)
	dispatcher := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	dispatcher.tick(t.Context())
	dispatcher.tick(t.Context())
	var count, paused int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE title='Healthy schedule'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("healthy schedule starved count%d %v", count, err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM recurring_issues WHERE id LIKE 'invalid-due-%' AND enabled=0 AND next_run>'2020-01-01T00:00:00Z'`).Scan(&paused); err != nil || paused != 100 {
		t.Fatalf("invalid templates remain due %d %v", paused, err)
	}
}
