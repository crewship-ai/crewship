package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV108_MissionProvenance verifies the additive ADD COLUMN
// migration that brings missions in line with pipelines on authorship
// tracking. Three things must hold:
//
//   - Legacy inserts that omit the new columns still succeed (NULL
//     defaults are valid).
//   - Inserts that populate the new columns land them correctly and
//     the partial indexes pick them up.
//   - The authored_via CHECK constraint accepts every documented
//     enum value and rejects an obvious typo.
//
// Mirrors the v107 gdpr_cascade test shape: seed FK targets, hit
// every CHECK enum positively, at least one negative.
func TestMigrateV108_MissionProvenance(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v108.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','C','c')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('ag1','cr1','ws1','Lead','lead','LEAD')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	// Legacy insert — provenance columns omitted, must succeed with NULLs.
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title)
		VALUES ('m_legacy','ws1','cr1','ag1','trace_legacy','Legacy mission')`); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}

	var chatID, runID, via *string
	if err := db.QueryRow(`SELECT author_chat_id, author_run_id, authored_via FROM missions WHERE id='m_legacy'`).
		Scan(&chatID, &runID, &via); err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if chatID != nil || runID != nil || via != nil {
		t.Errorf("legacy row should leave all provenance NULL; got chat=%v run=%v via=%v", chatID, runID, via)
	}

	// All documented authored_via enum values must be accepted.
	for i, v := range []string{"agent_tool_call", "user_api", "imported", "seed", "routine", "recurring"} {
		id := "m_via_" + v
		if _, err := db.Exec(`INSERT INTO missions
			(id, workspace_id, crew_id, lead_agent_id, trace_id, title,
			 author_chat_id, author_run_id, authored_via)
			VALUES (?, 'ws1','cr1','ag1', ?, ?, 'chat_'||?, 'run_'||?, ?)`,
			id, "trace_"+v, "Mission via "+v, i, i, v); err != nil {
			t.Errorf("authored_via=%s should be accepted: %v", v, err)
		}
	}

	// Negative: bogus authored_via must be rejected by CHECK.
	if _, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, authored_via)
		VALUES ('m_bad','ws1','cr1','ag1','trace_bad','Bad', 'fictional_value')`); err == nil {
		t.Error("expected CHECK violation on authored_via='fictional_value'")
	}

	// Indexed query: looking up by author_chat_id must find the row.
	var found string
	if err := db.QueryRow(`SELECT id FROM missions WHERE author_chat_id = 'chat_0'`).Scan(&found); err != nil {
		t.Fatalf("indexed lookup: %v", err)
	}
	if found != "m_via_agent_tool_call" {
		t.Errorf("indexed lookup returned %q, want m_via_agent_tool_call", found)
	}

	// Verify the partial indexes exist (CREATE INDEX IF NOT EXISTS).
	for _, idx := range []string{"idx_mission_chat", "idx_mission_run", "idx_mission_authored_via"} {
		var name string
		if err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).
			Scan(&name); err != nil {
			t.Errorf("index %s missing: %v", idx, err)
		}
	}
}

// TestMigrateV108_Idempotent — restart-resilience. Re-running Migrate
// on a v108-applied DB must be a no-op, not a duplicate-column error.
// The migrations array is processed sequentially with a _migrations
// row gate; this test pins the contract that v108 specifically does
// not regress the gate.
func TestMigrateV108_Idempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v108_idem.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	// Second Migrate on the same DB must succeed.
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("second Migrate (idempotency check): %v", err)
	}
}
