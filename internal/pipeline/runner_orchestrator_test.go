package pipeline

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// openAgentResolverTestDB sets up agents + crews tables minimally so
// the resolveAgentID JOIN can run. We don't pull in the full database
// migrate stack — just the columns the lookup hits.
func openAgentResolverTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE crews (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  name TEXT,
  deleted_at TEXT
);
CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  crew_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  deleted_at TEXT
);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// TestResolveAgentID_RejectsCrossWorkspace targets the security fix
// from the routines stabilization commit. resolveAgentID used to do a
// crew_id-only lookup (`WHERE crew_id = ? AND slug = ?`), so a
// pipeline whose AuthorCrewID pointed at a crew in workspace_B could
// reach that crew's agents while the calling workspace was workspace_A.
// The fix adds `JOIN crews ON crews.workspace_id = ?` so the lookup
// is rejected when the crew belongs to a different workspace.
func TestResolveAgentID_RejectsCrossWorkspace(t *testing.T) {
	db := openAgentResolverTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Two workspaces, each with a crew. Same crew slug in both for
	// extra confusion. Agent "tomas" exists in BOTH crews.
	if _, err := db.ExecContext(ctx, `
INSERT INTO crews (id, workspace_id, slug, name) VALUES
  ('crew_a_in_ws_a', 'ws_a', 'engineering', 'Eng A'),
  ('crew_b_in_ws_b', 'ws_b', 'engineering', 'Eng B');
INSERT INTO agents (id, crew_id, slug) VALUES
  ('agent_a_tomas', 'crew_a_in_ws_a', 'tomas'),
  ('agent_b_tomas', 'crew_b_in_ws_b', 'tomas');`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &OrchestratorRunner{db: db}

	// Lookup with matching workspace+crew → returns the right agent.
	id, err := r.resolveAgentID(ctx, "ws_a", "crew_a_in_ws_a", "tomas")
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if id != "agent_a_tomas" {
		t.Errorf("expected agent_a_tomas, got %s", id)
	}

	// Lookup with workspace_a but crew that belongs to workspace_b.
	// Without the JOIN guard, this would return agent_b_tomas (a
	// cross-workspace leak). With the fix, it must reject.
	id, err = r.resolveAgentID(ctx, "ws_a", "crew_b_in_ws_b", "tomas")
	if err == nil {
		t.Fatalf("CROSS-WORKSPACE LEAK: expected not-found error, got agent %q", id)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found error, got %v", err)
	}

	// Sanity: workspace_b + its own crew works.
	id, err = r.resolveAgentID(ctx, "ws_b", "crew_b_in_ws_b", "tomas")
	if err != nil {
		t.Fatalf("ws_b own-crew: %v", err)
	}
	if id != "agent_b_tomas" {
		t.Errorf("expected agent_b_tomas, got %s", id)
	}

	// Required-args validation
	if _, err := r.resolveAgentID(ctx, "", "crew_a_in_ws_a", "tomas"); err == nil {
		t.Error("expected error on empty workspace_id")
	}
	if _, err := r.resolveAgentID(ctx, "ws_a", "", "tomas"); err == nil {
		t.Error("expected error on empty crew_id")
	}
	if _, err := r.resolveAgentID(ctx, "ws_a", "crew_a_in_ws_a", ""); err == nil {
		t.Error("expected error on empty slug")
	}
}

// TestResolveAgentID_SkipsSoftDeletedRows verifies the deleted_at IS
// NULL guard on both agents and crews. A cleanup that soft-deleted
// the crew should make its agents unreachable too — the fix' JOIN
// includes `c.deleted_at IS NULL`.
func TestResolveAgentID_SkipsSoftDeletedRows(t *testing.T) {
	db := openAgentResolverTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
INSERT INTO crews (id, workspace_id, slug, deleted_at) VALUES
  ('live_crew',    'ws', 'eng', NULL),
  ('deleted_crew', 'ws', 'eng', '2024-01-01T00:00:00Z');
INSERT INTO agents (id, crew_id, slug, deleted_at) VALUES
  ('alive_agent',     'live_crew',    'tomas', NULL),
  ('agent_in_dead_crew', 'deleted_crew', 'tomas', NULL),
  ('dead_agent',      'live_crew',    'viktor', '2024-01-01T00:00:00Z');`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := &OrchestratorRunner{db: db}

	// Live crew + live agent → found
	if id, err := r.resolveAgentID(ctx, "ws", "live_crew", "tomas"); err != nil || id != "alive_agent" {
		t.Errorf("live: id=%s err=%v", id, err)
	}

	// Live crew + soft-deleted agent → not found
	if _, err := r.resolveAgentID(ctx, "ws", "live_crew", "viktor"); err == nil {
		t.Error("soft-deleted agent should not be reachable")
	}

	// Soft-deleted crew (with otherwise-live agent inside) → not found
	if _, err := r.resolveAgentID(ctx, "ws", "deleted_crew", "tomas"); err == nil {
		t.Error("agent inside soft-deleted crew should not be reachable")
	}
}
