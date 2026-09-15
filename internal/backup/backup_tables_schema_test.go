package backup_test

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// These two tests pin BackupTables against the real, fully-migrated schema in
// BOTH directions. Found via #2274: five names sat in BackupTables for months
// without a table behind them (`agent_runs`, `hooks`, `routines`, `schedules`,
// `webhooks`). DumpWorkspace and RestoreDump both skip unknown tables by design
// — that is how a bundle stays readable across schema revisions — so every
// one of them was a silent no-op that read like coverage.
//
// The existing guards do not close this:
//   - TestBackupIntent_TotalOverRealSchema walks schema → classification, so a
//     classified name with no table never trips it.
//   - TestBackupTableIntent_AllIncludedAreDumped walks intent → BackupTables,
//     and a dead name was present in both maps.
//
// Direction 1 (every listed name exists) is what #2274 asked for. Direction 2
// (every workspace-scoped table is listed or explicitly excluded) is the
// #1437 / #1444 shape read forwards: a real table nobody listed is silently
// not backed up. It is stricter than the totality guard because it keys on the
// `workspace_id` COLUMN — the strongest possible signal that rows are tenant
// data — and demands a written reason for each one left out, next to the name,
// in this file.

// workspaceTablesNotBundled is the explicit exclusion list for direction 2:
// every table in the migrated schema that carries a `workspace_id` column but
// is deliberately NOT in BackupTables, with the reason. Each entry is
// cross-checked below against intent.go (it must be an Exclude classification
// or on the deny-list there) so this list can never quietly disagree with the
// map restore-time drift detection uses.
//
// Adding a table with a `workspace_id` column makes this test fail until you
// either add it to BackupTables (FK-safe order, plus BackupTableIntent) or
// write its reason here. "It is not important" is not a reason; see #1437.
var workspaceTablesNotBundled = map[string]string{
	// --- Migration artefacts / archives (derived, never read by the app) ---
	"agent_runs_archive":       "one-off snapshot taken by v61 drop_agent_runs; the live data was folded into journal_entries, which rides the bundle",
	"journal_entries_archived": "compaction archive; BLOB/vector shape would corrupt under the TEXT-only round-trip path (see dbdump.go)",
	"journal_embeddings":       "derived vector index over journal_entries; rebuilt by the embedder, BLOB round-trip unsafe",

	// --- This instance's own backup / audit bookkeeping ---
	"audit_logs":              "instance audit trail; stays with the instance that produced it",
	"backup_catalog":          "catalogue of THIS instance's bundles; a restored copy would describe files the target does not hold",
	"backup_locks":            "in-flight backup mutex rows; process-local by definition",
	"backup_restore_origins":  "lineage evidence for DR resume authorisation (#1716); carrying it forward asserts a history the target never had",
	"crew_audit_log":          "crew action audit trail (operational)",
	"peer_card_audit":         "audit trail",
	"notification_deliveries": "delivery log for notification_channels (which DO ride the bundle); operational, not config",

	// --- Runtime state that regenerates on restore ---
	"agent_status":             "live agent status; every agent boots IDLE on the restored instance",
	"keeper_request_events":    "per-instance keeper decision history (projection of keeper_requests)",
	"memory_mutations":         "mutation ledger tied to idempotency keys of operations that do not travel; content and versions round-trip via memory_versions",
	"memory_revisions":         "revision anchors; a restored workspace reads cleanly at revision 0 and re-anchors on first write",
	"provider_device_logins":   "device-approval polling sessions owned by this process; never resume one on another instance",
	"routine_webhook_receipts": "receipt identities refer to this instance's pipeline runtime history",
	"session_mailbox":          "undelivered chat turns addressed to a live session that does not survive the restore; delivered ones are in workspace_conversation_messages",
	"webhook_deliveries":       "inbound delivery ledger incl. raw request bodies; dedup keys only mean something against work_items, which do not travel",
	"work_items":               "durable work queue; a restored bundle carrying queued work would RUN it again in the copy",
	"crew_messages":            "cross-crew message delivery log",
	"escalations":              "keeper escalation history",
	"mcp_tool_calls":           "MCP call telemetry",
	"oauth_states":             "OAuth CSRF nonces; ephemeral",
	"peer_conversations":       "sidecar peer Q&A history",
	"pipeline_run_idempotency": "dispatch dedup keys; runtime",
	"pipeline_signal_waits":    "in-flight signal waits; runtime",
}

// schemaTables returns every non-mechanical table in the migrated schema.
func schemaTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		if isMechanicalTable(name) {
			continue
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	return out
}

// tableHasColumn reports whether table declares a column named col.
func tableHasColumn(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    any
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == col {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return false
}

// TestBackupTables_EveryEntryExistsInSchema is direction 1: a name in
// BackupTables, BackupTableIntent or NonBackedUpTables that the migrated
// schema does not contain is a stale entry, and for BackupTables it is a
// silent no-op on every dump and restore (#2274).
func TestBackupTables_EveryEntryExistsInSchema(t *testing.T) {
	db := openMigratedDB(t)
	exists := map[string]bool{}
	for _, name := range schemaTables(t, db) {
		exists[name] = true
	}

	var dead []string
	for _, name := range backup.BackupTables {
		if !exists[name] {
			dead = append(dead, "BackupTables: "+name)
		}
	}
	for name := range backup.BackupTableIntent {
		if !exists[name] {
			dead = append(dead, "BackupTableIntent: "+name)
		}
	}
	for name := range backup.NonBackedUpTables {
		if !exists[name] {
			dead = append(dead, "NonBackedUpTables: "+name)
		}
	}
	if len(dead) > 0 {
		sort.Strings(dead)
		t.Fatalf("names with no table in the migrated schema:\n  %s\n\n"+
			"A BackupTables entry with no table behind it is skipped silently by both\n"+
			"DumpWorkspace and RestoreDump — it looks backed up and is not (#2274). If the\n"+
			"table was renamed, list the successor (FK-safe order) and delete the old name;\n"+
			"if it was dropped, delete the name from every list it appears in.",
			strings.Join(dead, "\n  "))
	}
}

// TestBackupTables_CoverWorkspaceScopedSchema is direction 2: every table in
// the migrated schema with a `workspace_id` column is either in BackupTables
// or in workspaceTablesNotBundled with a written reason — and the exclusion
// list itself is checked for staleness and for agreement with intent.go.
func TestBackupTables_CoverWorkspaceScopedSchema(t *testing.T) {
	db := openMigratedDB(t)

	listed := map[string]bool{}
	for _, name := range backup.BackupTables {
		listed[name] = true
	}

	workspaceScoped := map[string]bool{}
	var unaccounted []string
	for _, name := range schemaTables(t, db) {
		if !tableHasColumn(t, db, name, "workspace_id") {
			continue
		}
		workspaceScoped[name] = true
		if listed[name] {
			continue
		}
		if _, excluded := workspaceTablesNotBundled[name]; excluded {
			continue
		}
		unaccounted = append(unaccounted, name)
	}
	if len(unaccounted) > 0 {
		sort.Strings(unaccounted)
		t.Errorf("workspace-scoped tables (they carry a workspace_id column) that are neither in\n"+
			"BackupTables nor in workspaceTablesNotBundled:\n  %s\n\n"+
			"A workspace table nobody listed is silently not backed up — the #1437 / #1444\n"+
			"shape. Add it to BackupTables (FK-safe order) and BackupTableIntent, or add it to\n"+
			"workspaceTablesNotBundled in this file with the reason it must not ride a bundle.",
			strings.Join(unaccounted, "\n  "))
	}

	// The exclusion list must stay honest: no stale names, no name that is
	// also listed (a contradiction), and every reason must agree with the
	// classification intent.go gives the same table.
	for name, reason := range workspaceTablesNotBundled {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("workspaceTablesNotBundled[%q] has an empty reason", name)
		}
		if !workspaceScoped[name] {
			t.Errorf("workspaceTablesNotBundled[%q] is stale: no such workspace-scoped table in the migrated schema", name)
		}
		if listed[name] {
			t.Errorf("%q is in BOTH BackupTables and workspaceTablesNotBundled — pick one", name)
		}
		if intent, ok := backup.BackupTableIntent[name]; ok {
			if intent == backup.IntentInclude {
				t.Errorf("%q is IntentInclude in intent.go but excluded here — the two disagree", name)
			}
			continue
		}
		if _, denied := backup.NonBackedUpTables[name]; !denied {
			t.Errorf("%q is excluded here but has no classification in intent.go (BackupTableIntent or NonBackedUpTables)", name)
		}
	}
}

// TestBackupTables_SchemaGuardCanFail is the guard-of-the-guard: the column
// probe the coverage test keys on must see a real workspace_id column and must
// not see one where there is none, or the test above could never fire.
func TestBackupTables_SchemaGuardCanFail(t *testing.T) {
	db := openMigratedDB(t)
	if !tableHasColumn(t, db, "crews", "workspace_id") {
		t.Error("crews.workspace_id not detected — the coverage guard would skip every real table")
	}
	if tableHasColumn(t, db, "users", "workspace_id") {
		t.Error("users has no workspace_id, yet the probe reported one")
	}
	if tableHasColumn(t, db, "definitely_not_a_real_table_xyz", "workspace_id") {
		t.Error("probe reported a column on a table that does not exist")
	}
}
