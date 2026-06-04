package database

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrationOneLeadPerCrew_RemediatesExistingDuplicates proves the v110
// migration is safe to apply on an install that already holds two live LEADs
// in one crew (only possible via the now-closed create/promote TOCTOU). The
// remediation step must demote the extras BEFORE creating the unique index,
// otherwise CREATE UNIQUE INDEX would fail and abort the whole migration.
func TestMigrationOneLeadPerCrew_RemediatesExistingDuplicates(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "dup.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db.DB, newTestLogger()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Simulate a pre-fix install: drop the index so duplicates can exist.
	if _, err := db.DB.Exec(`DROP INDEX IF EXISTS idx_agents_one_lead_per_crew`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-rem', 'Rem', 'rem')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-rem', 'ws-rem', 'Rem', 'rem')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	insAgent := func(id string) {
		if _, err := db.DB.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
			cli_adapter, tool_profile, timeout_seconds, memory_enabled)
			VALUES (?, 'ws-rem', 'crew-rem', ?, ?, 'LEAD', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
			id, "Agent "+id, id); err != nil {
			t.Fatalf("seed lead %s: %v", id, err)
		}
	}
	insAgent("lead-first")  // earliest rowid → kept
	insAgent("lead-second") // demoted to AGENT

	// Run the v110 body directly (remediation + index) — must not error.
	if _, err := db.DB.Exec(migrationOneLeadPerCrew); err != nil {
		t.Fatalf("apply v110 over duplicate leads: %v", err)
	}

	var leads int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agents WHERE crew_id='crew-rem' AND agent_role='LEAD' AND deleted_at IS NULL`).Scan(&leads); err != nil {
		t.Fatalf("count leads: %v", err)
	}
	if leads != 1 {
		t.Fatalf("after remediation LEAD count = %d, want 1", leads)
	}
	var firstRole, secondRole string
	if err := db.DB.QueryRow(`SELECT agent_role FROM agents WHERE id='lead-first'`).Scan(&firstRole); err != nil {
		t.Fatalf("read lead-first: %v", err)
	}
	if err := db.DB.QueryRow(`SELECT agent_role FROM agents WHERE id='lead-second'`).Scan(&secondRole); err != nil {
		t.Fatalf("read lead-second: %v", err)
	}
	if firstRole != "LEAD" {
		t.Errorf("earliest lead role = %q, want LEAD (kept)", firstRole)
	}
	if secondRole != "AGENT" {
		t.Errorf("duplicate lead role = %q, want AGENT (demoted)", secondRole)
	}

	// Index now exists and blocks a fresh duplicate.
	if _, err := db.DB.Exec(`UPDATE agents SET agent_role='LEAD' WHERE id='lead-second'`); err == nil {
		t.Fatal("promoting a second lead succeeded, want UNIQUE constraint violation")
	}

	// Idempotent: re-running the body is a no-op, no error.
	if _, err := db.DB.Exec(migrationOneLeadPerCrew); err != nil {
		t.Fatalf("re-apply v110 (idempotent): %v", err)
	}
}
