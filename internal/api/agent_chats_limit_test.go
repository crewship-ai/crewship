package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /api/v1/agents/{id}/chats honours `limit`.
//
// The chat surface asks for ten rows per agent (PER_AGENT_CHAT_LIMIT) and
// the handler used to answer with its hard-coded hundred regardless — the
// client's fold ("13 more with Riley · Show all") can only be truthful if the
// page it holds is the page it asked for. Absent or zero keeps the old
// default; anything above the ceiling is clamped rather than refused.

func limitTList(t *testing.T, h *AgentHandler, wsID, userID, query string) []chatUnreadRow {
	t.Helper()
	rows, _ := limitTListWithTotal(t, h, wsID, userID, query)
	return rows
}

func limitTListWithTotal(t *testing.T, h *AgentHandler, wsID, userID, query string) ([]chatUnreadRow, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/agents/unread-ag/chats"+query, nil)
	req.SetPathValue("agentId", "unread-ag")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ListChats(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list%s status = %d, body=%s", query, rr.Code, rr.Body.String())
	}
	return uxDecode[[]chatUnreadRow](t, rr), rr.Header().Get("X-Total-Count")
}

func TestListChats_PagesWithOffsetAndTotal(t *testing.T) {
	db := setupTestDB(t)
	wsID, userID := unreadTSeed(t, db)
	for i := 0; i < 5; i++ {
		unreadTChat(t, db, fmt.Sprintf("chat-p%d", i), wsID, userID)
	}
	h := NewAgentHandler(db, newTestLogger())

	rows, total := limitTListWithTotal(t, h, wsID, userID, "?limit=2")
	if total != "5" || len(rows) != 2 {
		t.Errorf("page 1: rows=%d total=%q, want 2 of 5", len(rows), total)
	}
	rows, total = limitTListWithTotal(t, h, wsID, userID, "?limit=2&offset=4")
	if total != "5" || len(rows) != 1 {
		t.Errorf("last page: rows=%d total=%q, want 1 of 5", len(rows), total)
	}
	// The total is counted INSIDE the kind partition: a routine chat must not
	// inflate the number of direct conversations the fold announces.
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, created_by, status, message_count, origin)
		VALUES ('chat-p-rt', 'unread-ag', ?, ?, 'ACTIVE', 1, 'ROUTINE')`, wsID, userID)
	_, total = limitTListWithTotal(t, h, wsID, userID, "?kind=direct&limit=1")
	if total != "5" {
		t.Errorf("direct total = %q, want 5 (the routine chat is another kind)", total)
	}
	_, total = limitTListWithTotal(t, h, wsID, userID, "?limit=1")
	if total != "6" {
		t.Errorf("unpartitioned total = %q, want 6", total)
	}
}

func TestListChats_Limit(t *testing.T) {
	db := setupTestDB(t)
	wsID, userID := unreadTSeed(t, db)
	for i := 0; i < 5; i++ {
		unreadTChat(t, db, fmt.Sprintf("chat-l%d", i), wsID, userID)
	}
	h := NewAgentHandler(db, newTestLogger())

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"absent keeps the default page", "", 5},
		{"limit narrows the page", "?limit=2", 2},
		{"zero means default", "?limit=0", 5},
		{"negative means default", "?limit=-3", 5},
		{"above the ceiling is clamped, not refused", "?limit=100000", 5},
		{"limit composes with kind", "?limit=1&kind=direct", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(limitTList(t, h, wsID, userID, tc.query)); got != tc.want {
				t.Errorf("rows = %d, want %d", got, tc.want)
			}
		})
	}
}

// uxDecode unmarshals a recorder body into T or fails the test.
func uxDecode[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	return out
}
