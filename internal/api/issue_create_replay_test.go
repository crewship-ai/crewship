package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/featureflags"
)

func TestHumanInboxTaskCreateRetryIsOneCanonicalIssue(t *testing.T) {
	h, user, ws, crew, _, worker := newTestIssueHandler(t)
	enabled := true
	h.agentInboxEnabled = func(w string) bool { return enabled && w == ws }
	call := func(title string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/crews/"+crew+"/issues", bytes.NewBufferString(`{"title":"`+title+`","delegate_agent_id":"`+worker+`"}`)), user, ws, "OWNER")
		req.SetPathValue("crewId", crew)
		req.Header.Set("Idempotency-Key", "human-inbox-create-once")
		req.Header.Set(featureflags.AgentInboxRequestHeader, "1")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr
	}
	a := call("One task")
	if a.Code != 201 {
		t.Fatalf("first=%d %s", a.Code, a.Body.String())
	}
	enabled = false
	if rr := call("One task"); rr.Code != http.StatusNotFound {
		t.Fatalf("disabled retry=%d %s", rr.Code, rr.Body.String())
	}
	enabled = true
	b := call("One task")
	if b.Code != 201 || a.Body.String() != b.Body.String() || b.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("retry=%d %s; first=%s", b.Code, b.Body.String(), a.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate issue=%d %v", n, err)
	}
	if rr := call("Changed request"); rr.Code != 409 {
		t.Fatalf("conflicting idempotency key=%d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, ws, user)
	// Replay still goes through the current public authorization boundary.
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/issues", bytes.NewBufferString(`{"title":"One task","delegate_agent_id":"`+worker+`"}`)), user, ws, "VIEWER")
	req.SetPathValue("crewId", crew)
	req.Header.Set("Idempotency-Key", "human-inbox-create-once")
	req.Header.Set(featureflags.AgentInboxRequestHeader, "1")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 403 {
		t.Fatalf("revoked creator replay=%d %s", rr.Code, rr.Body.String())
	}
}

func TestHumanInboxTaskCreateReceiptRollsBackWithMutation(t *testing.T) {
	h, user, ws, crew, _, worker := newTestIssueHandler(t)
	h.agentInboxEnabled = func(w string) bool { return w == ws }
	call := func() *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/issues", bytes.NewBufferString(`{"title":"Atomic task","delegate_agent_id":"`+worker+`"}`)), user, ws, "OWNER")
		req.SetPathValue("crewId", crew)
		req.Header.Set("Idempotency-Key", "atomic-create")
		req.Header.Set(featureflags.AgentInboxRequestHeader, "1")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr
	}
	execOrFatal(t, h.db, `CREATE TRIGGER force_issue_activity_rollback BEFORE INSERT ON mission_activity BEGIN SELECT RAISE(ABORT,'forced rollback'); END`)
	if rr := call(); rr.Code < 500 {
		t.Fatalf("forced failure=%d %s", rr.Code, rr.Body.String())
	}
	for _, table := range []string{"missions", "agent_operations"} {
		var n int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial %s persisted=%d %v", table, n, err)
		}
	}
	execOrFatal(t, h.db, `DROP TRIGGER force_issue_activity_rollback`)
	if rr := call(); rr.Code != 201 {
		t.Fatalf("retry after rollback=%d %s", rr.Code, rr.Body.String())
	}
}

func TestHumanInboxTaskCreateConcurrentRetryAndLiveRevocation(t *testing.T) {
	h, user, ws, crew, _, worker := newTestIssueHandler(t)
	h.agentInboxEnabled = func(w string) bool { return w == ws }
	call := func() *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/issues", bytes.NewBufferString(`{"title":"Concurrent task","delegate_agent_id":"`+worker+`"}`)), user, ws, "OWNER")
		req.SetPathValue("crewId", crew)
		req.Header.Set("Idempotency-Key", "concurrent-create")
		req.Header.Set(featureflags.AgentInboxRequestHeader, "1")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr
	}
	var wg sync.WaitGroup
	answers := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); answers <- call() }()
	}
	wg.Wait()
	close(answers)
	var original string
	for rr := range answers {
		if rr.Code != 201 {
			t.Fatalf("concurrent create=%d %s", rr.Code, rr.Body.String())
		}
		if original != "" && original != rr.Body.String() {
			t.Fatal("retry changed canonical issue response")
		}
		original = rr.Body.String()
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("concurrent duplicate=%d %v", n, err)
	}
	// Even an already-authenticated request carrying the old OWNER context may
	// not replay a response after the membership is revoked in the database.
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER',capabilities='{}' WHERE workspace_id=? AND user_id=?`, ws, user)
	if rr := call(); rr.Code != 403 {
		t.Fatalf("stale auth context replay=%d %s", rr.Code, rr.Body.String())
	}
}

func TestHumanInboxUnmarkedIssueCreateUsesLegacyBehavior(t *testing.T) {
	h, user, workspace, crew, _, worker := newTestIssueHandler(t)
	h.agentInboxEnabled = func(string) bool { return true }
	for range 2 {
		req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/issues", bytes.NewBufferString(`{"title":"Ordinary task","delegate_agent_id":"`+worker+`"}`)), user, workspace, "OWNER")
		req.SetPathValue("crewId", crew)
		req.Header.Set("Idempotency-Key", "ordinary-create")
		response := httptest.NewRecorder()
		h.Create(response, req)
		if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "" {
			t.Fatalf("ordinary create changed: %d %s", response.Code, response.Body.String())
		}
	}
	var issues, operations int
	if err := h.db.QueryRow("SELECT (SELECT count(*) FROM missions),(SELECT count(*) FROM agent_operations)").Scan(&issues, &operations); err != nil || issues != 2 || operations != 0 {
		t.Fatalf("ordinary issues=%d inbox operation receipts=%d: %v", issues, operations, err)
	}
}

func TestHumanInboxDisabledRejectsMarkedStartAndChannelWithoutMutation(t *testing.T) {
	h, user, workspace, crew, _, _ := newTestIssueHandler(t)
	start := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/issues/start", nil), user, workspace, "OWNER")
	start.SetPathValue("crewId", crew)
	start.SetPathValue("identifier", "UNUSED")
	start.Header.Set(featureflags.AgentInboxRequestHeader, "1")
	response := httptest.NewRecorder()
	h.Start(response, start)
	if response.Code != http.StatusNotFound || !bytes.Contains(response.Body.Bytes(), []byte("human inbox is disabled")) {
		t.Fatalf("disabled explicit start was not fenced: %d %s", response.Code, response.Body.String())
	}
	send := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/conversations/messages", nil), user, workspace, "OWNER")
	send.Header.Set(featureflags.AgentInboxRequestHeader, "1")
	response = httptest.NewRecorder()
	(&WorkspaceConversationsHandler{}).Send(response, send)
	if response.Code != http.StatusNotFound || !bytes.Contains(response.Body.Bytes(), []byte("human inbox is disabled")) {
		t.Fatalf("disabled explicit send was not fenced: %d %s", response.Code, response.Body.String())
	}
}
