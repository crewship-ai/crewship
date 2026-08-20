package database

import (
	"strings"
	"testing"
)

// crew_templates.slug carried a global UNIQUE from v23 (`slug TEXT NOT NULL
// UNIQUE`), and v26 added workspace_id without rescoping it. The column that
// says which tenant owns a template therefore played no part in deciding
// whether its name was free: the first workspace to save `backend-team`
// consumed that name for every other workspace on the instance, and the only
// thing the resulting
//
//	UNIQUE constraint failed: crew_templates.slug
//
// can mean is "a row you cannot see already owns this".
//
// Uniqueness now sits where the rest of the schema puts it. Two partial
// indexes split the population: workspace-owned templates are unique per
// (workspace_id, slug); builtins — which seed_crew_templates.go writes with
// workspace_id NULL — are unique per slug among themselves, which is what
// keeps the seeder's update-then-insert idempotent.
//
// Precedence between the two halves is a workspace template SHADOWS the
// builtin of the same slug; that rule is enforced in internal/api and tested
// there, since it is a query concern rather than a schema one.

// seedCrewTemplateWorkspace creates one workspace under an id derived from
// suffix, so two calls produce two independent tenants.
func seedCrewTemplateWorkspace(t *testing.T, db *DB, suffix string) string {
	t.Helper()
	wsID := "ws_ctpl_" + suffix
	execMigrationFixture(t, db,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		wsID, "WS "+suffix, "ws-ctpl-"+suffix)
	return wsID
}

// insertCrewTemplate writes one template row and returns the error rather
// than failing, because both outcomes are asserted below. wsID == "" writes
// workspace_id NULL, which is the builtin shape.
func insertCrewTemplate(t *testing.T, db *DB, id, wsID, slug string, isBuiltin bool) error {
	t.Helper()
	var ws any
	if wsID != "" {
		ws = wsID
	}
	builtin := 0
	if isBuiltin {
		builtin = 1
	}
	_, err := db.Exec(`
		INSERT INTO crew_templates (id, name, slug, category, agents_json, is_builtin, workspace_id)
		VALUES (?, 'Backend Team', ?, 'GENERAL', '[]', ?, ?)`,
		id, slug, builtin, ws)
	return err
}

// TestCrewTemplateSlugIsWorkspaceScoped is the bug: two tenants, same slug,
// and the second one must land.
func TestCrewTemplateSlugIsWorkspaceScoped(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	wsA := seedCrewTemplateWorkspace(t, db, "a")
	wsB := seedCrewTemplateWorkspace(t, db, "b")

	if err := insertCrewTemplate(t, db, "ct_scope_a", wsA, "backend-team", false); err != nil {
		t.Fatalf("first workspace could not save backend-team: %v", err)
	}
	if err := insertCrewTemplate(t, db, "ct_scope_b", wsB, "backend-team", false); err != nil {
		t.Fatalf("second workspace could not save backend-team: %v\n"+
			"template slugs are a per-workspace namespace; a global one lets the first tenant "+
			"consume every ordinary name for every other tenant, and the constraint error "+
			"discloses that an invisible row owns it", err)
	}
}

// TestCrewTemplateSlugStillUniqueWithinWorkspace is the other half: scoping
// the constraint must not turn it off. Two templates sharing a slug inside ONE
// workspace make every by-slug lookup in the API ambiguous — that is the whole
// reason the constraint exists.
func TestCrewTemplateSlugStillUniqueWithinWorkspace(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	ws := seedCrewTemplateWorkspace(t, db, "dup")

	if err := insertCrewTemplate(t, db, "ct_dup_1", ws, "backend-team", false); err != nil {
		t.Fatalf("first backend-team: %v", err)
	}
	err := insertCrewTemplate(t, db, "ct_dup_2", ws, "backend-team", false)
	if err == nil {
		t.Fatal("a second backend-team in the SAME workspace was accepted — by-slug " +
			"template lookups scoped to a workspace are now ambiguous")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("second backend-team failed with %v, want a UNIQUE constraint violation", err)
	}
}

// TestCrewTemplateBuiltinSlugStillUnique pins the second partial index. The
// builtins share one namespace (workspace_id IS NULL) and MUST stay unique in
// it: SeedBuiltinCrewTemplates updates `WHERE slug = ? AND is_builtin = 1` and
// falls back to INSERT OR IGNORE, so a duplicate builtin slug would leave one
// of the two rows permanently stale — updated never, ignored always.
//
// Note the naive rewrite of this migration gets this wrong. A single
// UNIQUE(workspace_id, slug) with no partial predicate would NOT catch this:
// SQLite treats NULLs as distinct in a unique index, so every builtin row is
// unique to that index no matter how many share a slug.
func TestCrewTemplateBuiltinSlugStillUnique(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	if err := insertCrewTemplate(t, db, "ct_bi_1", "", "shipped-team", true); err != nil {
		t.Fatalf("first builtin shipped-team: %v", err)
	}
	err := insertCrewTemplate(t, db, "ct_bi_2", "", "shipped-team", true)
	if err == nil {
		t.Fatal("a second builtin (workspace_id NULL) with slug shipped-team was accepted — " +
			"the seeder's update-then-insert would leave one of them stale forever")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("second builtin failed with %v, want a UNIQUE constraint violation", err)
	}
}

// TestCrewTemplateSlugIndexShape asserts the schema directly, because the
// behavioural tests above all pass for the wrong reason if the constraint is
// simply gone. It also pins what the rebuild had to carry across by hand: a
// CREATE/copy/DROP/RENAME takes the old table's indexes down with it, and the
// FK action on workspace_id lives in the table definition that got rewritten.
func TestCrewTemplateSlugIndexShape(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	// The old global UNIQUE was a column constraint, so it existed as an
	// implicit autoindex that cannot be dropped — the rebuild is the only
	// thing that removes it, and its absence is what proves the rebuild ran.
	for _, name := range []string{"sqlite_autoindex_crew_templates_2", "idx_crew_templates_slug"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n); err != nil {
			t.Fatalf("read index %s: %v", name, err)
		}
		if n != 0 {
			t.Errorf("%s still exists — the global slug namespace was not rebuilt away", name)
		}
	}

	// Key columns come from PRAGMA index_info, not from a substring match on
	// the CREATE text: the index NAME contains "workspace", so a
	// strings.Contains over the SQL passes for an index keyed on slug alone.
	// The sibling assertion in migrate_mission_identifier_workspace_scope_test.go
	// records that exact mutation surviving the text-matching version.
	indexKeys := func(name string) []string {
		t.Helper()
		rows, err := db.Query(`PRAGMA index_info(` + quoteSQLiteIdent(name) + `)`)
		if err != nil {
			t.Fatalf("index_info(%s): %v", name, err)
		}
		defer rows.Close()
		var keys []string
		for rows.Next() {
			var seqno, cid int
			var col string
			if err := rows.Scan(&seqno, &cid, &col); err != nil {
				t.Fatalf("scan index_info(%s): %v", name, err)
			}
			keys = append(keys, col)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("index_info(%s) rows: %v", name, err)
		}
		return keys
	}

	for _, tc := range []struct {
		index     string
		wantKeys  []string
		wantWhere string
		unique    bool
	}{
		{"idx_crew_templates_workspace_slug", []string{"workspace_id", "slug"}, "WHERE WORKSPACE_ID IS NOT NULL", true},
		{"idx_crew_templates_global_slug", []string{"slug"}, "WHERE WORKSPACE_ID IS NULL", true},
		// The rebuild dropped these with the old table; they are here because
		// recreating them is the step easiest to lose.
		{"idx_crew_templates_category", []string{"category"}, "", false},
		{"idx_crew_templates_workspace", []string{"workspace_id"}, "", false},
	} {
		var ddl string
		if err := db.QueryRow(
			`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, tc.index).Scan(&ddl); err != nil {
			t.Errorf("%s is missing: %v", tc.index, err)
			continue
		}
		norm := strings.ToUpper(strings.Join(strings.Fields(ddl), " "))
		if got := strings.Contains(norm, "UNIQUE"); got != tc.unique {
			t.Errorf("%s UNIQUE = %v, want %v: %s", tc.index, got, tc.unique, norm)
		}
		if tc.wantWhere != "" && !strings.Contains(norm, tc.wantWhere) {
			t.Errorf("%s lost its partial predicate (%s): %s", tc.index, tc.wantWhere, norm)
		}
		keys := indexKeys(tc.index)
		if len(keys) != len(tc.wantKeys) {
			t.Errorf("%s keys = %v, want %v", tc.index, keys, tc.wantKeys)
			continue
		}
		for i := range tc.wantKeys {
			if keys[i] != tc.wantKeys[i] {
				t.Errorf("%s key %d = %q, want %q (keys: %v)", tc.index, i, keys[i], tc.wantKeys[i], keys)
			}
		}
	}
}

// TestCrewTemplateWorkspaceCascadeSurvivedRebuild pins the FK action the
// rebuild had to retype by hand. workspace_id was declared
// `REFERENCES workspaces(id) ON DELETE CASCADE` in v26; a rebuild that
// omitted the action would leave a deleted tenant's private templates behind
// as orphans — rows with a workspace_id pointing at nothing, invisible to
// every scoped query and carried into every backup.
func TestCrewTemplateWorkspaceCascadeSurvivedRebuild(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	ws := seedCrewTemplateWorkspace(t, db, "cascade")
	if err := insertCrewTemplate(t, db, "ct_cascade", ws, "doomed-team", false); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	execMigrationFixture(t, db, `DELETE FROM workspaces WHERE id = ?`, ws)

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM crew_templates WHERE workspace_id = ?`, ws).Scan(&n); err != nil {
		t.Fatalf("count templates after workspace delete: %v", err)
	}
	if n != 0 {
		t.Errorf("%d template(s) outlived their workspace — the rebuild dropped "+
			"ON DELETE CASCADE from workspace_id", n)
	}
}

// quoteSQLiteIdent wraps an identifier for interpolation into a PRAGMA, which
// takes no bound parameters. Inputs here are test-owned literals; the doubling
// is belt-and-braces so a future caller cannot turn this into an injection.
func quoteSQLiteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
