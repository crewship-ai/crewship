package backup_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestDumpWorkspace_PreviouslyDroppedTables is the behavioural guard for the
// backup data-loss fix. Eleven tables were marked IntentInclude in intent.go
// but absent from dbdump.go BackupTables, so DumpWorkspace (which iterates
// ONLY BackupTables) silently dropped them from every workspace bundle while
// the discovery drift test stayed green.
//
// This test seeds two tenants (ws_A, ws_B), dumps ws_A, and asserts for a
// representative set — including all four tables that have NO workspace_id
// column and therefore rely on a hand-rolled workspaceFilterSQL scope
// (chat_participants, chat_read_cursors, pipeline_routine_state,
// pipeline_run_step_outputs) — that:
//   - ws_A's row IS in the dump (no longer silently dropped), and
//   - ws_B's row is NOT (the scope clause is tenant-isolated, not a leak).
func TestDumpWorkspace_PreviouslyDroppedTables(t *testing.T) {
	db := openMigratedDB(t)
	ctx := context.Background()

	seedTenantWithDroppedTables(t, db, "A")
	seedTenantWithDroppedTables(t, db, "B")

	dump, err := backup.DumpWorkspace(ctx, db, "ws_A")
	if err != nil {
		t.Fatalf("dump ws_A: %v", err)
	}

	checks := []struct {
		table, idCol, want, deny string
	}{
		// No workspace_id column — hand-rolled scope clause (the risky ones).
		{"chat_participants", "chat_id", "ch_A", "ch_B"},
		{"chat_read_cursors", "chat_id", "ch_A", "ch_B"},
		{"pipeline_routine_state", "pipeline_id", "pl_A", "pl_B"},
		{"pipeline_run_step_outputs", "run_id", "rn_A", "rn_B"},
		// Direct workspace_id — generic scope, still previously dropped.
		{"pipeline_tags", "pipeline_id", "pl_A", "pl_B"},
		{"run_tags", "run_id", "rn_A", "rn_B"},
		{"pending_runs", "pipeline_id", "pl_A", "pl_B"},
		{"user_models", "workspace_id", "ws_A", "ws_B"},
		{"composio_settings", "workspace_id", "ws_A", "ws_B"},
		{"keeper_governance_settings", "workspace_id", "ws_A", "ws_B"},
		{"routine_step_overrides", "pipeline_id", "pl_A", "pl_B"},
	}
	for _, c := range checks {
		rows := dump.Tables[c.table]
		if len(rows) == 0 {
			t.Errorf("%s: dumped 0 rows — still silently dropped from backups", c.table)
			continue
		}
		var sawWant, sawDeny bool
		for _, r := range rows {
			switch fmt.Sprint(r[c.idCol]) {
			case c.want:
				sawWant = true
			case c.deny:
				sawDeny = true
			}
		}
		if !sawWant {
			t.Errorf("%s: ws_A row (%s=%s) missing — scope clause selects nothing", c.table, c.idCol, c.want)
		}
		if sawDeny {
			t.Errorf("%s: ws_B row (%s=%s) LEAKED into ws_A dump — scope not tenant-isolated", c.table, c.idCol, c.deny)
		}
	}
}

// seedTenantWithDroppedTables creates a minimal but FK-valid workspace
// "ws_<suffix>" plus one row in each previously-dropped table, all suffixed
// so the two tenants are distinguishable in the dump.
func seedTenantWithDroppedTables(t *testing.T, db *sql.DB, suffix string) {
	t.Helper()
	ctx := context.Background()
	ws := "ws_" + suffix
	usr := "u_" + suffix
	crew := "cr_" + suffix
	agent := "ag_" + suffix
	chat := "ch_" + suffix
	pl := "pl_" + suffix
	run := "rn_" + suffix

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %s [%s]: %v", suffix, q, err)
		}
	}

	// Parents.
	exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, usr, usr+"@e2e.test", "U "+suffix)
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, ws, "WS "+suffix, "ws-"+suffix)
	exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`, crew, ws, "Crew "+suffix, "crew-"+suffix)
	exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES (?, ?, ?, ?, ?, 'IDLE')`, agent, crew, ws, "Agent "+suffix, "agent-"+suffix)
	exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title, status) VALUES (?, ?, ?, ?, ?, 'ACTIVE')`, chat, agent, ws, usr, "Chat "+suffix)
	exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash) VALUES (?, ?, ?, ?, '{"name":"x","steps":[]}', ?)`, pl, ws, "pl-"+suffix, "PL "+suffix, "h_"+suffix)
	exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at) VALUES (?, ?, ?, ?, 'completed', '2026-01-01T00:00:00Z')`, run, ws, pl, "pl-"+suffix)

	// Previously-dropped tables (one row each).
	exec(`INSERT INTO chat_participants (chat_id, user_id, role) VALUES (?, ?, 'owner')`, chat, usr)
	exec(`INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at) VALUES (?, ?, '2026-01-01T00:00:00Z')`, usr, chat)
	exec(`INSERT INTO pipeline_routine_state (pipeline_id, schedule_id, key, value, updated_at) VALUES (?, '', 'watermark', ?, '2026-01-01T00:00:00Z')`, pl, "v_"+suffix)
	exec(`INSERT INTO pipeline_run_step_outputs (run_id, step_id, output, updated_at) VALUES (?, 's1', ?, '2026-01-01T00:00:00Z')`, run, "out_"+suffix)
	exec(`INSERT INTO pipeline_tags (pipeline_id, workspace_id, tag) VALUES (?, ?, ?)`, pl, ws, "tag_"+suffix)
	exec(`INSERT INTO run_tags (run_id, workspace_id, tag) VALUES (?, ?, ?)`, run, ws, "rtag_"+suffix)
	exec(`INSERT INTO pending_runs (id, workspace_id, pipeline_id, pipeline_slug, debounce_key, fire_at) VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z')`, "pr_"+suffix, ws, pl, "pl-"+suffix, "dk_"+suffix)
	exec(`INSERT INTO routine_step_overrides (pipeline_id, workspace_id, step_id) VALUES (?, ?, 's1')`, pl, ws)
	exec(`INSERT INTO user_models (id, workspace_id, user_id, user_slug, path) VALUES (?, ?, ?, ?, ?)`, "um_"+suffix, ws, usr, "us-"+suffix, "/models/"+suffix)
	exec(`INSERT INTO composio_settings (workspace_id, encrypted_api_key) VALUES (?, ?)`, ws, "enc_"+suffix)
	exec(`INSERT INTO keeper_governance_settings (workspace_id) VALUES (?)`, ws)
}
