package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestPeerConversationAgentFilterBeforePagination(t *testing.T) {
	db := setupTestDB(t)
	ws := seedTestWorkspace(t, db, seedTestUser(t, db))
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew', ?, 'Crew', 'crew')`, ws)
	for _, id := range []string{"a", "b", "c"} {
		execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, 'crew', ?, ?, ?)`, id, ws, id, id)
	}
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat', 'a', ?)`, ws)
	for i, pair := range [][2]string{{"a", "b"}, {"b", "a"}, {"b", "c"}} {
		execOrFatal(t, db, `INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at) VALUES (?, ?, 'crew', 'chat', ?, ?, 'question', 'COMPLETED', ?)`, fmt.Sprint(i), ws, pair[0], pair[1], fmt.Sprintf("2026-09-06T12:00:0%dZ", i))
	}
	h := NewQueryHandler(db, nil, nil, "token", slog.Default())
	for _, tc := range []struct{ query, workspace, want string }{
		{"agent_id=a&limit=1", ws, "1"},
		{"agent_id=a&limit=1&offset=1", ws, "0"},
		{"limit=1", ws, "2"},
		{"agent_id=missing&limit=1", ws, ""},
		{"agent_id=a&limit=1", "different-workspace", ""},
	} {
		t.Run(tc.query+tc.workspace, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/?"+tc.query, nil)
			req.SetPathValue("crewId", "crew")
			req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, tc.workspace))
			rr := httptest.NewRecorder()
			h.ListPeerConversations(rr, req)
			if rr.Code != 200 {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			var rows []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(rows) != 0 {
					t.Fatalf("unexpected rows: %+v", rows)
				}
				return
			}
			if len(rows) != 1 || rows[0].ID != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, rows)
			}
		})
	}
}
