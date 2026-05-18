package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// migrateChainSetup opens a fresh on-disk SQLite database under t.TempDir
// (so the foreign_keys=ON DSN pragma from database.Open actually applies —
// modernc.org/sqlite ignores the pragma silently for pure ":memory:" DSNs
// in some configurations) and runs the full migration chain. Tests in
// this file share the helper so a future schema rename only touches one
// fixture.
func migrateChainSetup(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v92.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// seedWorkspaceAndCrew creates the FK targets required to insert into
// memory_proposals (workspaces + crews). Returns the ids so callers can
// reference them in INSERTs without re-declaring the same string literal.
func seedWorkspaceAndCrew(t *testing.T, db *DB) (wsID, crewID string) {
	t.Helper()
	wsID, crewID = "ws_v92", "crew_v92"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, wsID, "WS92", wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`, crewID, wsID, "Crew92", crewID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return wsID, crewID
}

// TestMigrate_V92_ScoreJSONColumn_ExistsWithNotNullAndDefault asserts the
// v92 migration added memory_proposals.score_json with the documented
// NOT NULL constraint and DEFAULT '{}' literal (see
// migrate_consts_v92_proposal_scoring.go). Both properties are load-
// bearing: the explain endpoint blindly json.Decode's score_json on
// every proposal, so a NULL or absent column would crash it.
func TestMigrate_V92_ScoreJSONColumn_ExistsWithNotNullAndDefault(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	rows, err := db.Query(`PRAGMA table_info(memory_proposals)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	var found bool
	var gotType string
	var gotNotNull int
	var gotDflt sql.NullString
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == "score_json" {
			found = true
			gotType = strings.ToUpper(ctype)
			gotNotNull = notnull
			gotDflt = dflt
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	if !found {
		t.Fatalf("memory_proposals.score_json column missing after v92 migration")
	}
	if gotType != "TEXT" {
		t.Errorf("score_json type = %q, want TEXT", gotType)
	}
	if gotNotNull != 1 {
		t.Errorf("score_json notnull = %d, want 1 (NOT NULL)", gotNotNull)
	}
	// SQLite returns the DEFAULT expression verbatim including quotes.
	// The v92 migration declares DEFAULT '{}', so we expect the literal
	// "'{}'" string (with embedded single quotes).
	if !gotDflt.Valid || gotDflt.String != "'{}'" {
		t.Errorf("score_json default = %v, want '{}'", gotDflt)
	}
}

// TestMigrate_V92_ScoreJSONColumn_AcceptsArbitraryJSON verifies the
// column stores whatever JSON the consolidator's ScoreResult marshals
// to — the schema only declares TEXT NOT NULL DEFAULT '{}', no JSON1
// CHECK, so any TEXT is valid. The test rotates through an empty
// object, a nested object, and an array to confirm there's no implicit
// shape enforcement we'd want to know about.
func TestMigrate_V92_ScoreJSONColumn_AcceptsArbitraryJSON(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)

	payloads := map[string]string{
		"empty_object":   `{}`,
		"nested_object":  `{"recency":0.9,"signals":{"uniqueness":0.5}}`,
		"array_top":      `[1,2,3]`,
		"escaped_string": `{"note":"line1\nline2"}`,
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			id := "p_score_" + name
			if _, err := db.Exec(`
INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, score_json)
VALUES (?, ?, ?, ?, ?)`, id, wsID, crewID, "/tmp/"+id+".md", payload); err != nil {
				t.Fatalf("insert payload %q: %v", payload, err)
			}
			var got string
			if err := db.QueryRow(`SELECT score_json FROM memory_proposals WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if got != payload {
				t.Errorf("round-trip mismatch: got %q want %q", got, payload)
			}
		})
	}
}

// TestMigrate_V92_PreExistingV91Row_GetsDefaultScoreJSON simulates the
// upgrade path for an install that already had memory_proposals rows
// at v91 before the v92 migration ran. The DEFAULT '{}' literal is
// what guarantees those legacy rows satisfy the NOT NULL clause —
// SQLite back-fills it during ALTER TABLE ADD COLUMN. If a future
// rewrite dropped the DEFAULT, every existing row would still scan
// fine (column gets NULL on read) but the explain endpoint's
// json.Decode would crash on the NULL.
func TestMigrate_V92_PreExistingV91Row_GetsDefaultScoreJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v91-then-v92.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// Step 1: land migrations up to v91 ONLY — this is the schema
	// state every install on the previous release ran. applyMigrationsUpTo
	// lives in migrate_v89_test.go and is exported package-local.
	if err := applyMigrationsUpTo(ctx, db.DB, 91, logger); err != nil {
		t.Fatalf("apply v01..v91: %v", err)
	}

	// Step 2: seed a fully-shaped pre-v92 proposal row. No score_json
	// column exists on disk yet, so we explicitly omit it from the
	// INSERT — exactly the shape of a beta-installed row.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_pre', 'WS pre', 'ws_pre')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_pre', 'ws_pre', 'Crew pre', 'crew_pre')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`
INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, rules_count, entries_scanned)
VALUES ('p_pre_v92', 'ws_pre', 'crew_pre', '/tmp/p_pre.md', 3, 12)`); err != nil {
		t.Fatalf("insert pre-v92 proposal: %v", err)
	}

	// Step 3: apply v92. This MUST succeed against a populated table —
	// SQLite's ADD COLUMN ... NOT NULL only works if a DEFAULT is
	// provided (which v92 supplies), so a future rewrite that drops
	// the DEFAULT clause would surface here as "Cannot add a NOT NULL
	// column with default value NULL".
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("Migrate to v92 against populated v91 DB: %v", err)
	}

	// Step 4: the legacy row exists and its score_json defaulted to '{}'.
	var score string
	if err := db.QueryRow(`SELECT score_json FROM memory_proposals WHERE id = 'p_pre_v92'`).Scan(&score); err != nil {
		t.Fatalf("read score_json on legacy row: %v", err)
	}
	if score != "{}" {
		t.Errorf("legacy row score_json = %q, want %q", score, "{}")
	}

	// And other columns survived — paranoia check against a future
	// rewrite that swaps tables and shuffles columns under the hood.
	var rulesCount, entriesScanned int
	if err := db.QueryRow(`SELECT rules_count, entries_scanned FROM memory_proposals WHERE id = 'p_pre_v92'`).
		Scan(&rulesCount, &entriesScanned); err != nil {
		t.Fatalf("read legacy row payload: %v", err)
	}
	if rulesCount != 3 || entriesScanned != 12 {
		t.Errorf("legacy row payload mangled after v92: rules=%d entries=%d", rulesCount, entriesScanned)
	}
}

// TestMigrate_Chain_RunTwice_IsIdempotent guards against a regression
// where a migration's SQL stops being re-entrant. Migrate() tracks
// applied migrations in _migrations so the second call is supposed
// to be a no-op walk — but a migration that mistakenly executes
// CREATE TABLE foo (no IF NOT EXISTS) on every call would only fail
// the SECOND boot, which is exactly the kind of "works in dev"
// trap operators hit after a restart.
func TestMigrate_Chain_RunTwice_IsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "idem.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("second Migrate (idempotent re-run): %v", err)
	}

	// Sanity: _migrations row count must be exactly the slice length.
	// A duplicate INSERT would have failed on the PRIMARY KEY clause
	// — but if a future change replaces it with INSERT OR IGNORE,
	// this count assertion catches the silent double-write.
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&got); err != nil {
		t.Fatalf("count _migrations: %v", err)
	}
	if got != len(migrations) {
		t.Errorf("_migrations row count = %d, want %d (one row per migration)", got, len(migrations))
	}
}

// TestMigrate_V90_ProposalStatusCheck_RejectsTerminalWithoutDecidedFields
// re-verifies the audit-trail CHECK constraint on memory_proposals.status
// in the context of the full chain (the existing v90 test seeds the table
// at v90; this one runs through v92 to prove the rewrite migrations did
// not silently drop or relax the CHECK). approved/rejected with a NULL
// decided_at OR a NULL decided_by_user_id must be rejected — the manual
// dev1 verify script catches this; baked in here so CI does too.
func TestMigrate_V90_ProposalStatusCheck_RejectsTerminalWithoutDecidedFields(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, crewID := seedWorkspaceAndCrew(t, db)

	cases := []struct {
		name   string
		status string
		// nilAt / nilBy: true means leave the column unbound (NULL).
		nilAt, nilBy bool
	}{
		{name: "approved_null_decided_at", status: "approved", nilAt: true, nilBy: false},
		{name: "approved_null_decided_by", status: "approved", nilAt: false, nilBy: true},
		{name: "approved_both_null", status: "approved", nilAt: true, nilBy: true},
		{name: "rejected_null_decided_at", status: "rejected", nilAt: true, nilBy: false},
		{name: "rejected_null_decided_by", status: "rejected", nilAt: false, nilBy: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := "p_chk_" + c.name
			var atArg any = "2026-05-17T12:00:00Z"
			if c.nilAt {
				atArg = nil
			}
			var byArg any = "usr_op"
			if c.nilBy {
				byArg = nil
			}
			_, err := db.Exec(`
INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status, decided_at, decided_by_user_id)
VALUES (?, ?, ?, ?, ?, ?, ?)`, id, wsID, crewID, "/tmp/"+id+".md", c.status, atArg, byArg)
			if err == nil {
				t.Fatalf("expected CHECK violation for %s, got nil", c.name)
			}
			// isCheckConstraintErr is defined in
			// migrate_v90_memory_proposals_test.go — same package.
			if !isCheckConstraintErr(err) {
				t.Fatalf("expected CHECK violation, got %T: %v", err, err)
			}
		})
	}

	// Positive control: well-formed terminal row inserts cleanly. Without
	// this, a future bug that rejects ALL terminal rows would make every
	// negative case above pass for the wrong reason.
	if _, err := db.Exec(`
INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status, decided_at, decided_by_user_id)
VALUES ('p_chk_ok', ?, ?, '/tmp/ok.md', 'approved', '2026-05-17T12:00:00Z', 'usr_op')`,
		wsID, crewID); err != nil {
		t.Fatalf("positive control insert failed: %v", err)
	}
}

// TestMigrate_V91_MemoryVersionsWorkspaceFK_RejectsOrphan asserts that
// inserting a memory_versions row with a workspace_id that does not
// exist in workspaces fails with a FOREIGN KEY constraint failure.
//
// IMPORTANT: PRAGMA foreign_keys=ON must be active OUTSIDE a transaction.
// SQLite docs: "This pragma is a no-op within a transaction" — setting
// it inside BEGIN silently keeps FKs disabled for the duration. The
// database.Open helper enables it via DSN before any tx starts, so
// using Open here (not a hand-rolled connection) is load-bearing.
func TestMigrate_V91_MemoryVersionsWorkspaceFK_RejectsOrphan(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	// Confirm FKs are actually on — guards against a future Open()
	// refactor that drops the DSN pragma. A silent FK=OFF would make
	// the orphan INSERT below succeed and this test would report
	// "test fail" for the wrong reason; assert the pragma first.
	var fkOn int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fkOn); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if fkOn != 1 {
		t.Fatalf("foreign_keys pragma = %d, want 1 (Open should enable via DSN)", fkOn)
	}

	_, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
VALUES ('mv_orphan', 'ws_nonexistent', 'AGENT.md', 'agent', 'shaO', 100, 'a1', '/blobs/o')`)
	if err == nil {
		t.Fatalf("orphan workspace_id should violate FK, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed") {
		t.Fatalf("expected FOREIGN KEY constraint failure, got %T: %v", err, err)
	}
}

// TestMigrate_V91_TierCheck_AcceptsDocumentedAndRejectsUnknown locks in
// the exact tier vocabulary declared in migrate_consts_v91_memory_versions.go:
//
//	CHECK (tier IN ('agent','crew','workspace','pins','learned'))
//
// A typo'd 'garbage_tier' must be rejected; each documented tier must be
// accepted. The existing v91 test covers the same set with a positive-
// only loop; this test adds the negative case for 'garbage_tier' (which
// the task brief calls out by name) and asserts the failure is a CHECK
// violation, not e.g. NOT NULL on a different column.
func TestMigrate_V91_TierCheck_AcceptsDocumentedAndRejectsUnknown(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, _ := seedWorkspaceAndCrew(t, db)

	allowed := []string{"learned", "agent", "crew", "workspace", "pins"}
	for i, tier := range allowed {
		t.Run("accepts_"+tier, func(t *testing.T) {
			if _, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
VALUES (?, ?, 'AGENT.md', ?, ?, ?, 'a1', ?)`,
				"mv_ok_"+tier, wsID, tier, "sha-"+tier, 100+i, "/blobs/"+tier); err != nil {
				t.Errorf("documented tier %q rejected: %v", tier, err)
			}
		})
	}

	t.Run("rejects_garbage_tier", func(t *testing.T) {
		_, err := db.Exec(`
INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_by, payload_ref)
VALUES ('mv_bad', ?, 'AGENT.md', 'garbage_tier', 'shaBAD', 100, 'a1', '/blobs/bad')`, wsID)
		if err == nil {
			t.Fatalf("garbage_tier should violate CHECK, got nil")
		}
		if !isCheckConstraintErr(err) {
			t.Fatalf("expected CHECK violation, got %T: %v", err, err)
		}
	})
}

// TestMigrate_V90_InboxItemsKindCheck_AcceptsMemoryConsolidation
// re-verifies — at the full-chain level rather than at v90 in isolation
// — that the inbox_items.kind CHECK constraint admits 'memory_consolidation'
// after the v90 rebuild-by-recreate ran. A later migration (v91/v92)
// that ever ALTER'd or recreated inbox_items without preserving the
// widened CHECK would surface here.
func TestMigrate_V90_InboxItemsKindCheck_AcceptsMemoryConsolidation(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, _ := seedWorkspaceAndCrew(t, db)

	// Positive: memory_consolidation must be allowed.
	if _, err := db.Exec(`
INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
VALUES ('ibx_mc_chain', ?, 'memory_consolidation', 'prop_chain', 'Memory consolidation proposal')`, wsID); err != nil {
		t.Fatalf("insert memory_consolidation: %v", err)
	}

	// Negative paranoia check: a clearly bogus kind must still fail
	// with a CHECK error. Catches a future rewrite that swaps the
	// CHECK for a NOT NULL or drops it entirely.
	_, err := db.Exec(`
INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
VALUES ('ibx_chain_bad', ?, 'no_such_kind', 'src', 't')`, wsID)
	if err == nil {
		t.Fatalf("bogus kind should violate CHECK, got nil")
	}
	if !isCheckConstraintErr(err) {
		t.Fatalf("expected CHECK violation, got %T: %v", err, err)
	}
}
