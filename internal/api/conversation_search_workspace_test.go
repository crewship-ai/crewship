package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// stubMultiConversationSearcher satisfies BOTH ConversationSearcher and
// MultiAgentConversationSearcher, the way the production convStoreAdapter
// does, so the handler's scope selection can be observed from the outside.
type stubMultiConversationSearcher struct {
	stubConversationSearcher
	gotAgentIDs []string
	multiHits   []ConversationSearchHit
	multiErr    error
}

func (s *stubMultiConversationSearcher) SearchConversationsAcross(_ context.Context, agentIDs []string, query string, limit int) ([]ConversationSearchHit, error) {
	s.gotAgentIDs = append([]string(nil), agentIDs...)
	s.gotQuery = query
	s.gotLimit = limit
	return s.multiHits, s.multiErr
}

// TestConversationSearch_Scopes is the scope + tenancy table. The palette
// searches everything the caller can see, so agent_id had to become
// optional; the workspace it falls back to must come from the request
// context, never from the body.
func TestConversationSearch_Scopes(t *testing.T) {
	hitFor := func(id, agent string) ConversationSearchHit {
		return ConversationSearchHit{
			ID: id, SessionID: "chat-" + id, AgentID: agent,
			Role: "user", Content: "deploy the pipeline", Timestamp: "2026-06-01T00:00:00Z",
		}
	}

	tests := []struct {
		name string
		// wsID is the workspace on the request context (the caller's).
		wsID      string
		body      string
		multiHits []ConversationSearchHit
		agentHits []ConversationSearchHit
		wantCode  int
		wantCount int
		// wantAgentIDs, when non-nil, is the exact set the workspace-scoped
		// searcher must have been handed.
		wantAgentIDs []string
		wantQuery    string
	}{
		{
			name:         "workspace scope spans every agent in the workspace",
			wsID:         "ws1",
			body:         `{"query":"deploy"}`,
			multiHits:    []ConversationSearchHit{hitFor("m1", "agent1"), hitFor("m2", "agent2")},
			wantCode:     http.StatusOK,
			wantCount:    2,
			wantAgentIDs: []string{"agent1", "agent2"},
			wantQuery:    "deploy",
		},
		{
			name:      "agent scope still narrows to one agent",
			wsID:      "ws1",
			body:      `{"agent_id":"agent1","query":"deploy"}`,
			agentHits: []ConversationSearchHit{hitFor("m1", "agent1")},
			wantCode:  http.StatusOK,
			wantCount: 1,
			wantQuery: "deploy",
		},
		{
			name:     "explicit cross-tenant agent id is a 404",
			wsID:     "ws2",
			body:     `{"agent_id":"agent1","query":"deploy"}`,
			wantCode: http.StatusNotFound,
		},
		{
			// ws2's caller runs the SAME workspace-wide query. Even when the
			// searcher misbehaves and returns ws1 rows, the handler must not
			// hand them over: the scope is the caller's own agents.
			name:         "another workspace sees none of ws1's history",
			wsID:         "ws2",
			body:         `{"query":"deploy"}`,
			multiHits:    []ConversationSearchHit{hitFor("m1", "agent1"), hitFor("m2", "agent2")},
			wantCode:     http.StatusOK,
			wantCount:    0,
			wantAgentIDs: []string{"agent3"},
		},
		{
			name:     "empty query is refused in workspace scope too",
			wsID:     "ws1",
			body:     `{"query":"   "}`,
			wantCode: http.StatusBadRequest,
		},
		{
			// FTS5 operators are user text, not syntax. The query builder
			// must survive them and pass them through untouched.
			name:         "fts syntax characters reach the searcher verbatim",
			wsID:         "ws1",
			body:         `{"query":"\"deploy\" OR (rm* NEAR/3 -db) AND ^x"}`,
			multiHits:    []ConversationSearchHit{hitFor("m1", "agent1")},
			wantCode:     http.StatusOK,
			wantCount:    1,
			wantAgentIDs: []string{"agent1", "agent2"},
			wantQuery:    `"deploy" OR (rm* NEAR/3 -db) AND ^x`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			seedConvAgent(t, db, "ws1", "agent1")
			if _, err := db.Exec(
				`INSERT INTO agents (id, workspace_id, name, slug, cli_adapter, status)
				 VALUES ('agent2','ws1','Second','second','CLAUDE_CODE','ACTIVE')`); err != nil {
				t.Fatalf("seed agent2: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws2','W2','ws2')`); err != nil {
				t.Fatalf("seed ws2: %v", err)
			}
			if _, err := db.Exec(
				`INSERT INTO agents (id, workspace_id, name, slug, cli_adapter, status)
				 VALUES ('agent3','ws2','Foreign','foreign','CLAUDE_CODE','ACTIVE')`); err != nil {
				t.Fatalf("seed agent3: %v", err)
			}

			stub := &stubMultiConversationSearcher{multiHits: tc.multiHits}
			stub.hits = tc.agentHits
			h := NewConversationHandler(db, stub)

			rec := doSearch(t, h, tc.wsID, tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}

			var resp struct {
				Count int                     `json:"count"`
				Hits  []ConversationSearchHit `json:"hits"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Count != tc.wantCount || len(resp.Hits) != tc.wantCount {
				t.Errorf("count = %d (hits %d), want %d", resp.Count, len(resp.Hits), tc.wantCount)
			}
			if tc.wantQuery != "" && stub.gotQuery != tc.wantQuery {
				t.Errorf("query forwarded = %q, want %q", stub.gotQuery, tc.wantQuery)
			}
			if tc.wantAgentIDs != nil {
				got := strings.Join(stub.gotAgentIDs, ",")
				want := strings.Join(tc.wantAgentIDs, ",")
				if got != want {
					t.Errorf("workspace scope searched %q, want %q", got, want)
				}
			}
		})
	}
}

// TestConversationSearch_WorkspaceHitsCarryAgentIdentity: the palette links
// to /chat/<agentSlug>?session=<chatId> and labels the row with the agent's
// name, so an id alone is not enough — the handler resolves both from the
// workspace it already looked up.
func TestConversationSearch_WorkspaceHitsCarryAgentIdentity(t *testing.T) {
	db := setupTestDB(t)
	seedConvAgent(t, db, "ws1", "agent1")
	stub := &stubMultiConversationSearcher{multiHits: []ConversationSearchHit{
		{ID: "m1", SessionID: "chat-1", AgentID: "agent1", Role: "user", Content: "deploy", Timestamp: "2026-06-01T00:00:00Z"},
	}}
	h := NewConversationHandler(db, stub)

	rec := doSearch(t, h, "ws1", `{"query":"deploy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Hits []ConversationSearchHit `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(resp.Hits))
	}
	if resp.Hits[0].AgentSlug != "agent" || resp.Hits[0].AgentName != "Agent" {
		t.Errorf("hit identity = slug %q name %q, want slug \"agent\" name \"Agent\"",
			resp.Hits[0].AgentSlug, resp.Hits[0].AgentName)
	}
}

// TestConversationSearch_AgentScopeCarriesIdentityToo keeps the two scopes'
// wire shapes identical, so the CLI and the palette parse one envelope.
func TestConversationSearch_AgentScopeCarriesIdentity(t *testing.T) {
	db := setupTestDB(t)
	seedConvAgent(t, db, "ws1", "agent1")
	stub := &stubMultiConversationSearcher{}
	stub.hits = []ConversationSearchHit{
		{ID: "m1", SessionID: "chat-1", AgentID: "agent1", Role: "user", Content: "deploy", Timestamp: "2026-06-01T00:00:00Z"},
	}
	h := NewConversationHandler(db, stub)

	rec := doSearch(t, h, "ws1", `{"agent_id":"agent1","query":"deploy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"agent_slug":"agent"`) {
		t.Errorf("agent-scoped hit missing agent_slug: %s", rec.Body.String())
	}
}

// TestConversationSearch_WorkspaceWithNoAgents answers with an empty result
// rather than an error — an empty workspace is a legitimate state, and the
// palette must not treat it as a failure.
func TestConversationSearch_WorkspaceWithNoAgents(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','ws1')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	stub := &stubMultiConversationSearcher{}
	h := NewConversationHandler(db, stub)

	rec := doSearch(t, h, "ws1", `{"query":"deploy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hits":[]`) {
		t.Errorf("want empty hits, got %s", rec.Body.String())
	}
	if stub.gotAgentIDs != nil {
		t.Errorf("searcher called with %v for an agentless workspace", stub.gotAgentIDs)
	}
}

// TestConversationSearch_WorkspaceScopeNeedsMultiSearcher: a searcher that
// can only do one agent at a time cannot answer a workspace-wide query, and
// says so with the same 503 an unconfigured feature uses — it does not
// silently return the first agent's history.
func TestConversationSearch_WorkspaceScopeNeedsMultiSearcher(t *testing.T) {
	db := setupTestDB(t)
	seedConvAgent(t, db, "ws1", "agent1")
	h := NewConversationHandler(db, &stubConversationSearcher{})

	rec := doSearch(t, h, "ws1", `{"query":"deploy"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body = %s", rec.Code, rec.Body.String())
	}
}
