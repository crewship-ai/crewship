package database

import (
	"strings"
	"testing"
)

// 20260915221827_user_model_provenance.sql (#1693): the evidence store beside
// the operator-model file. The table is append-only by convention — nothing
// updates a row, a re-sync of the same key adds one — so the schema has to
// ALLOW several rows per (workspace, slug, key), refuse a source type outside
// usermodel.SourceType's vocabulary, and follow the workspace and the user
// away like user_models does. Each of those is a property a purge or a read
// relies on, and each is proved here rather than assumed.
func TestMigrate_UserModelProvenance_AppendOnlyEvidenceRows(t *testing.T) {
	db := openMigratedTestDB(t)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", strings.SplitN(strings.TrimSpace(q), "\n", 2)[0], err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_prov', 'P', 'prov'), ('ws_other', 'O', 'other')`)
	mustExec(`INSERT INTO users (id, email) VALUES ('u_prov', 'prov@x')`)

	// Two rows for the same fact: the correction is an append, so the second
	// insert must succeed rather than trip a UNIQUE.
	mustExec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type, recorded_at)
		VALUES ('p1', 'ws_prov', 'u_prov', 'slug1', 'role', 'runs the platform team',
		        'I run the platform team', 'msg-1', 'stated', '2026-09-01T00:00:00.000Z')`)
	mustExec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type, recorded_at)
		VALUES ('p2', 'ws_prov', 'u_prov', 'slug1', 'role', 'leads the platform team',
		        'I lead the platform team now', 'msg-2', 'stated', '2026-09-02T00:00:00.000Z')`)

	var newest string
	if err := db.QueryRow(`SELECT quote FROM user_model_provenance
		WHERE workspace_id = 'ws_prov' AND user_slug = 'slug1' AND key = 'role'
		ORDER BY recorded_at DESC LIMIT 1`).Scan(&newest); err != nil {
		t.Fatalf("read newest: %v", err)
	}
	if newest != "I lead the platform team now" {
		t.Errorf("newest row per key = %q, want the second insert", newest)
	}

	// recorded_at defaults to the fixed-width T-form every ordered column in
	// this schema uses; a space-form default would sort before every
	// existing row and make "newest" mean "oldest".
	mustExec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, source_type)
		VALUES ('p3', 'ws_prov', 'u_prov', 'slug1', 'timezone', 'UTC+1', 'I am on UTC+1', 'stated')`)
	var at, msg string
	if err := db.QueryRow(`SELECT recorded_at, message_id FROM user_model_provenance WHERE id = 'p3'`).Scan(&at, &msg); err != nil {
		t.Fatalf("read default: %v", err)
	}
	if len(at) != len("2026-09-01T00:00:00.000Z") || at[10] != 'T' || !strings.HasSuffix(at, "Z") {
		t.Errorf("recorded_at default = %q, want fixed-width millisecond T-form", at)
	}
	if msg != "" {
		t.Errorf("message_id default = %q, want empty (no FK, no NULL)", msg)
	}

	// The vocabulary is closed at the schema, not just in the Go constant.
	if _, err := db.Exec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, source_type)
		VALUES ('bad', 'ws_prov', 'u_prov', 'slug1', 'role', 'v', 'q', 'guessed')`); err == nil {
		t.Errorf("source_type outside stated/observed/inferred was accepted")
	}

	// Workspace-scoped: the workspace going takes its evidence with it, and
	// the other workspace's rows are untouched.
	mustExec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, source_type)
		VALUES ('p4', 'ws_other', 'u_prov', 'slug2', 'role', 'v', 'q', 'stated')`)
	mustExec(`DELETE FROM workspaces WHERE id = 'ws_prov'`)
	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM user_model_provenance WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", where, err)
		}
		return n
	}
	if n := count(`workspace_id = 'ws_prov'`); n != 0 {
		t.Errorf("%d row(s) survived their workspace's deletion", n)
	}
	if n := count(`workspace_id = 'ws_other'`); n != 1 {
		t.Errorf("the other workspace's row count = %d, want 1", n)
	}

	// And the user going takes the rest.
	mustExec(`DELETE FROM users WHERE id = 'u_prov'`)
	if n := count(`user_id = 'u_prov'`); n != 0 {
		t.Errorf("%d row(s) survived their user's deletion", n)
	}
}

// The index the readers and purges lean on exists under the name the
// migration gives it — a renamed or dropped index is a full scan of an
// append-only table on every `privacy user-model list`.
func TestMigrate_UserModelProvenance_IndexesExist(t *testing.T) {
	db := openMigratedTestDB(t)
	for _, name := range []string{"idx_user_model_provenance_fact", "idx_user_model_provenance_user"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n); err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("index %s missing", name)
		}
	}
}
