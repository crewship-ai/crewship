package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/work"
)

func TestInboxWorkFailureUsesCurrentSourceAudience(t *testing.T) {
	hIssue, user, ws, crew, lead, _ := newTestIssueHandler(t)
	db := hIssue.db
	execOrFatal(t, db, `INSERT INTO chats(id,agent_id,workspace_id,created_by,visibility)
		VALUES('failure-chat',?,?,?,'private')`, lead, ws, user)
	store := work.NewStore(db)
	const runID = "failure-audience-run"
	if _, _, err := store.BeginHostChat(t.Context(), work.HostChatRequest{
		RunID: runID, WorkspaceID: ws, CrewID: crew, AgentID: lead,
		ChatID: "failure-chat", UserID: user, TriggerMessageID: "message",
	}); err != nil {
		t.Fatal(err)
	}
	exit := 1
	if err := store.FinishHostChat(t.Context(), runID, work.HostChatResult{Status: "FAILED", ObservedExit: &exit}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO inbox_items(id,workspace_id,kind,source_id,target_user_id,title,state,payload_json)
		VALUES('private-failure',?,'failed_run',?,?,'Agent run failed','unread','{"subkind":"work_run_failure"}')`, ws, runID, user)
	h := NewInboxHandler(db, newTestLogger(), nil)
	get := func() int {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/inbox/private-failure", nil), user, ws, "OWNER")
		req.SetPathValue("id", "private-failure")
		rec := httptest.NewRecorder()
		h.Get(rec, req)
		return rec.Code
	}
	if status := get(); status != http.StatusOK {
		t.Fatalf("authorized get: %d", status)
	}
	execOrFatal(t, db, "DELETE FROM chats WHERE id='failure-chat'")
	if status := get(); status != http.StatusNotFound {
		t.Fatalf("revoked get: %d", status)
	}
	status, body := inboxQueryGet(t, h, user, ws, "/api/v1/inbox", url.Values{})
	if status != http.StatusOK || bytes.Contains(body, []byte("private-failure")) {
		t.Fatalf("revoked projection still listed: %d %s", status, body)
	}
}

func TestInboxWorkFailureSourceUsesIndexedLookups(t *testing.T) {
	db := setupTestDB(t)
	visibility, args := inboxVisibilityClause("recipient", "OWNER")
	rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN SELECT id FROM inbox_items WHERE workspace_id=?"+visibility,
		append([]any{"workspace"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "SCAN wa") || strings.Contains(detail, "SCAN wi") ||
			strings.Contains(detail, "SCAN o ") || strings.Contains(detail, "SCAN journal_entries") {
			t.Errorf("unexpected source scan: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
