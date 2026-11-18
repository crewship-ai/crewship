package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestHumanInboxTaskCreateRetryIsOneCanonicalIssue(t *testing.T) {
	h, user, ws, crew, _, worker := newTestIssueHandler(t)
	call := func(title string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/crews/"+crew+"/issues", bytes.NewBufferString(`{"title":"`+title+`","delegate_agent_id":"`+worker+`"}`)), user, ws, "OWNER")
		req.SetPathValue("crewId", crew)
		req.Header.Set("Idempotency-Key", "human-inbox-create-once")
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr
	}
	a := call("One task")
	if a.Code != 201 {
		t.Fatalf("first=%d %s", a.Code, a.Body.String())
	}
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
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != 403 {
		t.Fatalf("revoked creator replay=%d %s", rr.Code, rr.Body.String())
	}
}

func TestHumanInboxTaskCreateReceiptRollsBackWithMutation(t *testing.T) {
	h, user, ws, crew, _, worker := newTestIssueHandler(t)
	call := func() *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/issues", bytes.NewBufferString(`{"title":"Atomic task","delegate_agent_id":"`+worker+`"}`)), user, ws, "OWNER")
		req.SetPathValue("crewId", crew)
		req.Header.Set("Idempotency-Key", "atomic-create")
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
	call := func() *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/issues", bytes.NewBufferString(`{"title":"Concurrent task","delegate_agent_id":"`+worker+`"}`)), user, ws, "OWNER")
		req.SetPathValue("crewId", crew)
		req.Header.Set("Idempotency-Key", "concurrent-create")
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
