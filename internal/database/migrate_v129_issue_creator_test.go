package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV129_IssueCreatorAttribution asserts the v129 column adds apply
// cleanly: both creator-identity columns exist, default NULL on legacy-style
// inserts, accept values, and a second Migrate run is a no-op.
func TestMigrateV129_IssueCreatorAttribution(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v129.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed the FK chain a mission row needs.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_v129', 'WS129', 'ws-v129')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_v129', 'ws_v129', 'C', 'c-v129')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES ('agt_v129', 'ws_v129', 'crew_v129', 'A', 'a-v129', 'LEAD')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Legacy-style insert (no creator columns) — both must read back NULL.
	if _, err := db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES ('msn_v129a', 'ws_v129', 'crew_v129', 'agt_v129', 't1', 'legacy', 'BACKLOG', 'issue', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert legacy mission: %v", err)
	}
	var authorAgent, createdByUser sql.NullString
	if err := db.QueryRow(`SELECT author_agent_id, created_by_user_id FROM missions WHERE id = 'msn_v129a'`).
		Scan(&authorAgent, &createdByUser); err != nil {
		t.Fatalf("read creator columns: %v", err)
	}
	if authorAgent.Valid || createdByUser.Valid {
		t.Errorf("legacy row creator columns = %v/%v, want NULL/NULL", authorAgent, createdByUser)
	}

	// Attributed insert — values round-trip.
	if _, err := db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type,
		    author_agent_id, authored_via, created_at, updated_at)
		VALUES ('msn_v129b', 'ws_v129', 'crew_v129', 'agt_v129', 't2', 'attributed', 'BACKLOG', 'issue',
		    'agt_v129', 'agent_tool_call', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert attributed mission: %v", err)
	}
	if err := db.QueryRow(`SELECT author_agent_id FROM missions WHERE id = 'msn_v129b'`).Scan(&authorAgent); err != nil {
		t.Fatalf("read author_agent_id: %v", err)
	}
	if authorAgent.String != "agt_v129" {
		t.Errorf("author_agent_id = %q, want agt_v129", authorAgent.String)
	}

	// Idempotency: second migrate must not error.
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
