package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logging"
)

func seedSearchAudience(t *testing.T, db *sql.DB, pairs ...[2]string) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO users(id,email) VALUES ('search-user','search-user@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES ('search-ws','Search','search-ws')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('search-member','search-ws','search-user','MEMBER')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range pairs {
		if _, err := db.Exec(`INSERT OR IGNORE INTO agents(id,workspace_id,name,slug,status) VALUES (?,'search-ws',?,?,'IDLE')`, pair[1], pair[1], pair[1]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO chats(id,agent_id,workspace_id,created_by,status) VALUES (?,?,'search-ws','search-user','ACTIVE')`, pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
}

// TestConvStoreAdapter_SearchConversationsAcross proves the adapter the
// server wires at boot can answer a WORKSPACE-scoped query — the shape ⌘K
// sends, where the user names no agent — and that the agent set it is given
// is the whole scope.
func TestConvStoreAdapter_SearchConversationsAcross(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedSearchAudience(t, db, [2]string{"sess1", "agentA"}, [2]string{"sess2", "agentB"}, [2]string{"sess3", "agentC"})
	logger := logging.New("error", "json", nil)
	store := conversation.NewStore(t.TempDir(), logger, conversation.WithDB(db))
	t.Cleanup(store.Close)

	for _, m := range []struct{ session, agent, content string }{
		{"sess1", "agentA", "please deploy the staging pipeline tonight"},
		{"sess2", "agentB", "the deploy finished, pipeline is green"},
		{"sess3", "agentC", "deploy talk from an agent outside the workspace"},
	} {
		if err := store.Append(context.Background(), m.session, conversation.Message{
			ID:        "m_" + m.session,
			AgentID:   m.agent,
			Role:      conversation.RoleUser,
			Content:   m.content,
			Timestamp: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("append %s: %v", m.session, err)
		}
	}

	a := &convStoreAdapter{store: store}
	// The adapter must satisfy the interface the handler type-asserts for,
	// or the route answers 503 for every workspace-scoped query.
	var _ goapi.MultiAgentConversationSearcher = a

	hits, err := a.SearchConversationsAcross(context.Background(), []string{"agentA", "agentB"}, "search-user", "deploy", 10)
	if err != nil {
		t.Fatalf("SearchConversationsAcross: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2: %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.AgentID == "agentC" {
			t.Errorf("agent outside the set leaked: %+v", h)
		}
		if _, perr := time.Parse(time.RFC3339Nano, h.Timestamp); perr != nil {
			t.Errorf("timestamp %q is not RFC3339Nano: %v", h.Timestamp, perr)
		}
	}
}
