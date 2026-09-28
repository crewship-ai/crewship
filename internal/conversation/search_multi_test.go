package conversation

import (
	"context"
	"strings"
	"testing"
)

// TestSearchAgents_SpansEveryAgentInTheSet is the substrate under the
// workspace-scoped API search: ONE ranked query over a set of agents, not N
// agent-scoped queries merged by a caller that can no longer see the scores.
func TestSearchAgents_SpansEveryAgentInTheSet(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess-a", "agent-a", RoleUser, "deploy the staging pipeline", "")
	appendMsg(t, s, "sess-b", "agent-b", RoleAssistant, "the deploy pipeline is green", "")
	appendMsg(t, s, "sess-c", "agent-c", RoleUser, "deploy nothing, this agent is out of scope", "")

	hits, err := s.SearchAgents(context.Background(), []string{"agent-a", "agent-b"}, "deploy", 10)
	if err != nil {
		t.Fatalf("SearchAgents: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2: %+v", len(hits), hits)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.AgentID] = true
	}
	if !seen["agent-a"] || !seen["agent-b"] {
		t.Errorf("hits did not span both agents: %+v", hits)
	}
	if seen["agent-c"] {
		t.Errorf("hit from an agent outside the set leaked: %+v", hits)
	}
}

func TestSearchAgentsForUser_FiltersPrivateChatsBeforeLimit(t *testing.T) {
	s := newSearchStore(t)
	for _, stmt := range []string{
		`INSERT INTO users(id,email) VALUES ('search-h1','search-h1@example.test'),('search-h2','search-h2@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES ('search-ws','Search','search-ws')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('search-m1','search-ws','search-h1','MEMBER'),('search-m2','search-ws','search-h2','MEMBER')`,
		`INSERT INTO agents(id,workspace_id,name,slug,status) VALUES ('search-agent','search-ws','Search','search-agent','IDLE')`,
		`INSERT INTO chats(id,agent_id,workspace_id,created_by,status) VALUES ('search-c1','search-agent','search-ws','search-h1','ACTIVE'),('search-c2','search-agent','search-ws','search-h2','ACTIVE')`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	appendMsg(t, s, "search-c1", "search-agent", RoleUser, "orion alpha private", "")
	appendMsg(t, s, "search-c2", "search-agent", RoleUser, "orion bravo private", "")

	for _, tc := range []struct{ user, own string }{{"search-h1", "search-c1"}, {"search-h2", "search-c2"}} {
		hits, err := s.SearchAgentsForUser(t.Context(), []string{"search-agent"}, tc.user, "orion", 1)
		if err != nil || len(hits) != 1 || hits[0].SessionID != tc.own {
			t.Errorf("user=%s hits=%+v err=%v, want own chat %s under LIMIT 1", tc.user, hits, err, tc.own)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM workspace_members WHERE workspace_id='search-ws' AND user_id='search-h2'`); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchAgentsForUser(t.Context(), []string{"search-agent"}, "search-h2", "orion", 10)
	if err != nil || len(hits) != 0 {
		t.Errorf("revoked user retained search hits: %+v %v", hits, err)
	}
}

// TestSearchAgents_ScopeIsTheSet: an empty set is an error, never an
// unfiltered scan of every conversation in the database.
func TestSearchAgents_ScopeIsTheSet(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess-a", "agent-a", RoleUser, "deploy the staging pipeline", "")

	for _, ids := range [][]string{nil, {}, {"  "}} {
		if _, err := s.SearchAgents(context.Background(), ids, "deploy", 10); err == nil {
			t.Errorf("SearchAgents(%v) returned no error; an empty scope must not scan everything", ids)
		}
	}
}

// TestSearchAgents_FTSOperatorsAreLiteralText: ⌘K forwards whatever the user
// typed. FTS5 operators in that text must be matched as words, not parsed as
// syntax — the query builder must not blow up on them.
func TestSearchAgents_FTSOperatorsAreLiteralText(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess-a", "agent-a", RoleUser, `we discussed "quoted" tokens and NEAR misses`, "")

	for _, q := range []string{
		`"`,
		`"deploy" OR (rm* NEAR/3 -db)`,
		`AND`,
		`^start`,
		`a:b`,
		`col*`,
		`"" ""`,
	} {
		hits, err := s.SearchAgents(context.Background(), []string{"agent-a"}, q, 10)
		if err != nil {
			t.Errorf("SearchAgents(%q) errored: %v", q, err)
			continue
		}
		_ = hits // a match is not required; surviving the query builder is
	}

	// The quoted phrase is still findable as literal text.
	hits, err := s.SearchAgents(context.Background(), []string{"agent-a"}, `"quoted" tokens`, 10)
	if err != nil {
		t.Fatalf("SearchAgents(literal phrase): %v", err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Content, "quoted") {
		t.Errorf("literal phrase search returned %+v", hits)
	}
}

// TestSearchAgents_EmptyQueryRefused keeps the agent-scoped contract: a
// whitespace query is an error, not an unbounded dump of history.
func TestSearchAgents_EmptyQueryRefused(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess-a", "agent-a", RoleUser, "deploy the staging pipeline", "")
	if _, err := s.SearchAgents(context.Background(), []string{"agent-a"}, "   ", 10); err == nil {
		t.Error("empty query accepted")
	}
}

// TestSearch_StillAgentScoped guards the single-agent entry point through
// the shared implementation: one agent in, one agent's history out.
func TestSearch_StillAgentScopedAfterMulti(t *testing.T) {
	s := newSearchStore(t)
	appendMsg(t, s, "sess-a", "agent-a", RoleUser, "deploy the staging pipeline", "")
	appendMsg(t, s, "sess-b", "agent-b", RoleUser, "deploy the other pipeline", "")

	hits, err := s.Search(context.Background(), "agent-a", "deploy", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].AgentID != "agent-a" {
		t.Errorf("agent scope broke: %+v", hits)
	}
	if _, err := s.Search(context.Background(), "  ", "deploy", 10); err == nil {
		t.Error("Search with an empty agent id accepted")
	}
}
