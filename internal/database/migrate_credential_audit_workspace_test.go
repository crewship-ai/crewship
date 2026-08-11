package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// credentialAuditBackfillVersion is the backfill migration under test. Named
// here so the marker-clearing re-apply cannot drift from the filename.
const credentialAuditBackfillVersion = 20260810153105

// credential_audit had no workspace column, so scoping it to a tenant meant
// joining through credentials — a shape no index could serve, on the one audit
// table with no retention sweep. The column exists so the admin audit view can
// be answered by (workspace_id, occurred_at DESC) instead of gathering every
// matching row and sorting it before LIMIT discards almost all of them.
//
// Three things have to hold, and each fails silently rather than loudly:
// the column and its index have to be the shape the query plans against,
// rows written before the column existed have to be attributed, and re-running
// the backfill must not rewrite a row that already has an answer.

func credentialAuditTestDB(t *testing.T) (*DB, context.Context, *slog.Logger) {
	t.Helper()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "cred-audit-ws.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db, ctx, silent
}

// seedCredentialAuditTenants builds two workspaces, each with a credential, so
// a scoped read has something to exclude as well as something to return.
func seedCredentialAuditTenants(t *testing.T, db *DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_a','A','ws-a'), ('ws_b','B','ws-b')`,
		`INSERT INTO users (id, email) VALUES ('u_ca','ca@example.com')`,
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		   VALUES ('cred_a','ws_a','github-a','enc','u_ca'),
		          ('cred_b','ws_b','github-b','enc','u_ca')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

// TestCredentialAuditWorkspaceColumnShape pins the DDL the query plans
// against. An index on the wrong columns, or in the wrong order, still
// answers every query — just slowly, which is the whole defect this
// migration exists to remove, so it would pass any correctness-only test.
func TestCredentialAuditWorkspaceColumn(t *testing.T) {
	t.Parallel()
	// Shares one migrated schema with the scoped-read checks below:
	// credentialAuditTestDB runs the whole ~200-migration chain per call, and
	// this package already pays for dozens of those.
	db, _, _ := credentialAuditTestDB(t)
	seedCredentialAuditTenants(t, db)

	t.Run("column exists and is nullable", func(t *testing.T) {
		var found bool
		var notNull int
		rows, err := db.Query(`PRAGMA table_info(credential_audit)`)
		if err != nil {
			t.Fatalf("table_info: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var cid, nn, pk int
			var name, ctype string
			var dflt *string
			if err := rows.Scan(&cid, &name, &ctype, &nn, &dflt, &pk); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if name == "workspace_id" {
				found, notNull = true, nn
			}
		}
		if !found {
			t.Fatal("credential_audit.workspace_id is missing")
		}
		// Nullable is not an oversight: SQLite cannot ADD COLUMN a NOT NULL
		// without a non-NULL default, and a REFERENCES clause added by ALTER
		// TABLE is only legal when the column defaults to NULL.
		if notNull != 0 {
			t.Errorf("workspace_id is NOT NULL; ALTER TABLE ADD COLUMN cannot produce that shape with a REFERENCES clause")
		}
	})

	t.Run("index is (workspace_id, occurred_at) in that order", func(t *testing.T) {
		var cols []string
		rows, err := db.Query(`PRAGMA index_info(idx_credential_audit_workspace_time)`)
		if err != nil {
			t.Fatalf("index_info: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var seqno, cid int
			var name *string
			if err := rows.Scan(&seqno, &cid, &name); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if name != nil {
				cols = append(cols, *name)
			}
		}
		if len(cols) != 2 || cols[0] != "workspace_id" || cols[1] != "occurred_at" {
			t.Errorf("index columns = %v, want [workspace_id occurred_at] — leading with occurred_at would make the scope predicate a scan again", cols)
		}
	})

	t.Run("the foreign key to workspaces is present", func(t *testing.T) {
		var found bool
		rows, err := db.Query(`PRAGMA foreign_key_list(credential_audit)`)
		if err != nil {
			t.Fatalf("foreign_key_list: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, seq int
			var table, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if from == "workspace_id" && table == "workspaces" {
				found = true
				if onDelete != "CASCADE" {
					t.Errorf("ON DELETE %s, want CASCADE to match audit_logs", onDelete)
				}
			}
		}
		if !found {
			t.Error("no foreign key from credential_audit.workspace_id to workspaces")
		}
	})

	t.Run("a scoped read returns only that workspace", func(t *testing.T) {
		scopedRead(t, db)
	})
}

// TestCredentialAuditWorkspaceBackfill covers rows written before the column
// existed. They cannot be created directly — the migration has already run by
// the time the test has a database — so the pre-migration state is
// reconstructed by NULLing the column, clearing the backfill's ledger row and
// migrating again. Same technique as
// TestMigrateBackfillCrewContainerSizes.
func TestCredentialAuditWorkspaceBackfill(t *testing.T) {
	t.Parallel()
	db, ctx, silent := credentialAuditTestDB(t)
	seedCredentialAuditTenants(t, db)

	if _, err := db.Exec(`
		INSERT INTO credential_audit (id, credential_id, event_type, occurred_at)
		VALUES ('ca_old_a','cred_a','USE','2026-01-01T00:00:00.000Z'),
		       ('ca_old_b','cred_b','USE','2026-01-02T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed audit rows: %v", err)
	}
	// Reconstruct the pre-migration state.
	if _, err := db.Exec(`UPDATE credential_audit SET workspace_id = NULL`); err != nil {
		t.Fatalf("null out workspace_id: %v", err)
	}

	reapply := func() {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM _migrations WHERE version = ?`, credentialAuditBackfillVersion); err != nil {
			t.Fatalf("clear migration marker: %v", err)
		}
		if err := Migrate(ctx, db.DB, silent); err != nil {
			t.Fatalf("re-Migrate (backfill): %v", err)
		}
	}
	workspaceOf := func(id string) string {
		t.Helper()
		var ws *string
		if err := db.QueryRow(`SELECT workspace_id FROM credential_audit WHERE id = ?`, id).Scan(&ws); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if ws == nil {
			return ""
		}
		return *ws
	}

	reapply()

	tests := []struct {
		row  string
		want string
	}{
		{"ca_old_a", "ws_a"},
		{"ca_old_b", "ws_b"},
	}
	for _, tc := range tests {
		if got := workspaceOf(tc.row); got != tc.want {
			t.Errorf("%s: workspace_id = %q, want %q — the row is invisible to a workspace-scoped audit read until it is attributed", tc.row, got, tc.want)
		}
	}

	// Idempotent, and non-destructive. A second run must not rewrite a row
	// that already carries an answer — including a deliberately odd one. If
	// the WHERE clause were dropped, this row would be silently "corrected"
	// back to its credential's workspace and the guard would be gone with no
	// test failing.
	if _, err := db.Exec(`UPDATE credential_audit SET workspace_id = 'ws_b' WHERE id = 'ca_old_a'`); err != nil {
		t.Fatalf("hand-set workspace: %v", err)
	}
	reapply()
	if got := workspaceOf("ca_old_a"); got != "ws_b" {
		t.Errorf("re-running the backfill rewrote an already-populated row (%q); it must only touch NULLs", got)
	}
}

// scopedRead is the reason the column exists: a read scoped to one workspace
// must return that workspace's rows and only those, using the column rather
// than a join.
func scopedRead(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO credential_audit (id, credential_id, workspace_id, event_type, occurred_at)
		VALUES ('ca_a1','cred_a','ws_a','USE','2026-03-01T00:00:00.000Z'),
		       ('ca_a2','cred_a','ws_a','ROTATE','2026-03-02T00:00:00.000Z'),
		       ('ca_b1','cred_b','ws_b','USE','2026-03-03T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tests := []struct {
		workspace string
		want      int
	}{
		{"ws_a", 2},
		{"ws_b", 1},
		{"ws_missing", 0},
	}
	for _, tc := range tests {
		t.Run(tc.workspace, func(t *testing.T) {
			var n int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM credential_audit WHERE workspace_id = ?`, tc.workspace).Scan(&n); err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != tc.want {
				t.Errorf("workspace %s: %d rows, want %d", tc.workspace, n, tc.want)
			}
		})
	}

	// The point of the index is that the scoped, time-ordered read the audit
	// page issues is answered by walking it, not by sorting the match set.
	// Assert on the plan, because a correct-but-slow query passes every
	// row-count assertion above.
	var plan string
	rows, err := db.Query(`EXPLAIN QUERY PLAN
		SELECT id FROM credential_audit WHERE workspace_id = ? ORDER BY occurred_at DESC LIMIT 50`, "ws_a")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_credential_audit_workspace_time") {
		t.Errorf("scoped audit read does not use idx_credential_audit_workspace_time:\n%s", plan)
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("scoped audit read still sorts the whole match set — the index is not satisfying ORDER BY:\n%s", plan)
	}
}
