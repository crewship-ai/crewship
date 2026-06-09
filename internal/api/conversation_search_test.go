package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubConversationSearcher struct {
	gotAgent string
	gotQuery string
	gotLimit int
	hits     []ConversationSearchHit
	err      error
}

func (s *stubConversationSearcher) SearchConversations(_ context.Context, agentID, query string, limit int) ([]ConversationSearchHit, error) {
	s.gotAgent = agentID
	s.gotQuery = query
	s.gotLimit = limit
	return s.hits, s.err
}

// seedAgent inserts a workspace + agent so agentExists passes.
func seedConvAgent(t *testing.T, db *sql.DB, wsID, agentID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'W', ?)`, wsID, wsID); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, cli_adapter, status)
		 VALUES (?, ?, 'Agent', 'agent', 'CLAUDE_CODE', 'ACTIVE')`,
		agentID, wsID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func doSearch(t *testing.T, h *ConversationHandler, wsID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations/search", strings.NewReader(body))
	if wsID != "" {
		req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, wsID))
	}
	rec := httptest.NewRecorder()
	h.Search(rec, req)
	return rec
}

func TestConversationSearchHandler_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	seedConvAgent(t, db, "ws1", "agent1")
	stub := &stubConversationSearcher{hits: []ConversationSearchHit{
		{ID: "m1", SessionID: "s1", AgentID: "agent1", Role: "user", Content: "deploy staging", Timestamp: "2026-06-01T00:00:00Z"},
	}}
	h := NewConversationHandler(db, stub)

	rec := doSearch(t, h, "ws1", `{"agent_id":"agent1","query":"deploy","limit":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.gotAgent != "agent1" || stub.gotQuery != "deploy" || stub.gotLimit != 7 {
		t.Errorf("forwarded agent=%q query=%q limit=%d", stub.gotAgent, stub.gotQuery, stub.gotLimit)
	}
	var resp struct {
		Count int                     `json:"count"`
		Hits  []ConversationSearchHit `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 || len(resp.Hits) != 1 || resp.Hits[0].Content != "deploy staging" {
		t.Errorf("unexpected envelope: %+v", resp)
	}
}

func TestConversationSearchHandler_Errors(t *testing.T) {
	db := setupTestDB(t)
	seedConvAgent(t, db, "ws1", "agent1")

	t.Run("no_workspace", func(t *testing.T) {
		h := NewConversationHandler(db, &stubConversationSearcher{})
		rec := doSearch(t, h, "", `{"agent_id":"agent1","query":"x"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("nil_searcher_503", func(t *testing.T) {
		h := NewConversationHandler(db, nil)
		rec := doSearch(t, h, "ws1", `{"agent_id":"agent1","query":"x"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("bad_json", func(t *testing.T) {
		h := NewConversationHandler(db, &stubConversationSearcher{})
		rec := doSearch(t, h, "ws1", `{"agent_id":`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("missing_agent_id", func(t *testing.T) {
		h := NewConversationHandler(db, &stubConversationSearcher{})
		rec := doSearch(t, h, "ws1", `{"query":"x"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("missing_query", func(t *testing.T) {
		h := NewConversationHandler(db, &stubConversationSearcher{})
		rec := doSearch(t, h, "ws1", `{"agent_id":"agent1","query":"  "}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("agent_not_in_workspace_404", func(t *testing.T) {
		h := NewConversationHandler(db, &stubConversationSearcher{})
		rec := doSearch(t, h, "ws1", `{"agent_id":"ghost","query":"x"}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("cross_tenant_agent_404", func(t *testing.T) {
		// agent1 lives in ws1; a caller in ws2 must not reach it.
		if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws2','W2','ws2')`); err != nil {
			t.Fatalf("seed ws2: %v", err)
		}
		h := NewConversationHandler(db, &stubConversationSearcher{})
		rec := doSearch(t, h, "ws2", `{"agent_id":"agent1","query":"x"}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("cross-tenant status = %d, want 404", rec.Code)
		}
	})

	t.Run("searcher_error_400", func(t *testing.T) {
		h := NewConversationHandler(db, &stubConversationSearcher{err: errInvalidQuery})
		rec := doSearch(t, h, "ws1", `{"agent_id":"agent1","query":"x"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

// TestConversationSearchHandler_NilHitsNormalized verifies a nil hits slice
// from the searcher renders as an empty JSON array, not null.
func TestConversationSearchHandler_NilHitsNormalized(t *testing.T) {
	db := setupTestDB(t)
	seedConvAgent(t, db, "ws1", "agent1")
	h := NewConversationHandler(db, &stubConversationSearcher{hits: nil})
	rec := doSearch(t, h, "ws1", `{"agent_id":"agent1","query":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"hits":[]`) {
		t.Errorf("nil hits not normalized to []: %s", rec.Body.String())
	}
}

// TestConversationSearchHandler_AgentLookupDBError exercises the 500 path:
// a closed DB makes agentExists return an operational error rather than a
// clean not-found.
func TestConversationSearchHandler_AgentLookupDBError(t *testing.T) {
	db := setupTestDB(t)
	seedConvAgent(t, db, "ws1", "agent1")
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	h := NewConversationHandler(db, &stubConversationSearcher{})
	rec := doSearch(t, h, "ws1", `{"agent_id":"agent1","query":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestWithConversationSearchOption confirms the RouterOption wires the
// searcher onto the Router so the route handler is backed.
func TestWithConversationSearchOption(t *testing.T) {
	r := &Router{}
	WithConversationSearch(&stubConversationSearcher{})(r)
	if r.convSearcher == nil {
		t.Error("WithConversationSearch did not set convSearcher")
	}
}

var errInvalidQuery = &searchErr{"query is required"}

type searchErr struct{ msg string }

func (e *searchErr) Error() string { return e.msg }
