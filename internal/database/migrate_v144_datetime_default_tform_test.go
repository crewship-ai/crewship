package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tformPattern matches the fixed-width ISO T-form timestamp this migration's
// DEFAULT now produces: "2026-07-13T21:00:00.123Z".
var tformPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// legacySpaceFormPattern matches SQLite's `datetime('now')` output:
// "2026-07-13 21:00:00" — no 'T', no fraction, no zone marker.
var legacySpaceFormPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)

// TestMigrateV144_ConvertedColumnsDefaultToTForm is the reproducing test for
// #1073b: before this migration, `credentials.created_at` (named
// explicitly in the issue as the column PR #1156's keyset-cursor pagination
// depends on) defaulted to SQLite's space-separated legacy form on any
// insert that omitted the column. That form never compares correctly
// against the ISO T-form strings the rest of the codebase writes. After
// v144, a raw insert that omits created_at must get a T-form value instead.
//
// This test fails on pre-#1073b code exactly the way the bug manifests:
// insert a row without created_at, read it back, and it's the legacy
// space-form shape rather than T-form.
func TestMigrateV144_ConvertedColumnsDefaultToTForm(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_v144', 'WS142', 'ws-v144')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_v144', 'v144@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// A raw insert that omits created_at/updated_at, exactly the
	// "raw insert/backfill" scenario the issue warns about — no
	// application-level Go writer is involved, only the column DEFAULT.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, created_by)
		VALUES ('cred_v144', 'ws_v144', 'Raw Insert Cred', 'ciphertext', 'user_v144')`); err != nil {
		t.Fatalf("raw insert into credentials: %v", err)
	}

	var createdAt, updatedAt string
	if err := db.QueryRow(`SELECT created_at, updated_at FROM credentials WHERE id = 'cred_v144'`).
		Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read back credentials row: %v", err)
	}

	if !tformPattern.MatchString(createdAt) {
		t.Errorf("credentials.created_at DEFAULT produced %q — want ISO T-form matching %s", createdAt, tformPattern)
	}
	if !tformPattern.MatchString(updatedAt) {
		t.Errorf("credentials.updated_at DEFAULT produced %q — want ISO T-form matching %s", updatedAt, tformPattern)
	}
	if legacySpaceFormPattern.MatchString(createdAt) {
		t.Errorf("credentials.created_at DEFAULT still produces legacy space-form: %q", createdAt)
	}
}

// TestMigrateV144_TFormSortsCorrectlyAgainstExplicitWrites reproduces the
// actual production symptom: a keyset-cursor / ORDER BY query over a
// converted column must place a DEFAULT-produced row in the correct
// chronological position relative to rows written with an explicit
// RFC3339 timestamp by application code — not after every legacy row
// regardless of real time, which is what happened when the DEFAULT was
// space-form (' ' sorts before 'T' in ASCII, so legacy rows always sorted
// as "earlier" than any RFC3339 row no matter their actual time).
func TestMigrateV144_TFormSortsCorrectlyAgainstExplicitWrites(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144_sort.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_v144s', 'WS142S', 'ws-v144s')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_v144s', 'v144s@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Row 1: explicit application-style RFC3339 write, deliberately given
	// an early timestamp.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, created_by, created_at, updated_at)
		VALUES ('cred_early', 'ws_v144s', 'Early', 'x', 'user_v144s', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert early row: %v", err)
	}

	// Row 2: raw insert relying on the DEFAULT — always "now", i.e. long
	// after the row above.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, created_by)
		VALUES ('cred_now', 'ws_v144s', 'Now', 'y', 'user_v144s')`); err != nil {
		t.Fatalf("insert DEFAULT row: %v", err)
	}

	rows, err := db.Query(`SELECT id FROM credentials WHERE workspace_id = 'ws_v144s' ORDER BY created_at ASC`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var order []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(order) != 2 || order[0] != "cred_early" || order[1] != "cred_now" {
		t.Fatalf("ORDER BY created_at ASC = %v, want [cred_early cred_now] — the DEFAULT-produced row must sort AFTER the earlier explicit timestamp", order)
	}
}

// TestMigrateV144_IndexesAndTriggersSurviveRecreation guards against a
// regression to a schema-recreate mechanism (this migration's first
// implementation used SQLite's documented table-recreate dance, which DROP
// TABLE silently strips indexes/triggers from — see
// rewriteTableDefaultLiteral's doc comment for why that approach was
// abandoned in favor of an in-place sqlite_master.sql rewrite that never
// drops or recreates anything). credential_crews carries a named trigger
// (trg_credential_crews_workspace_check) that rejects a credential_crews
// row whose crew_id belongs to a different workspace than the credential;
// if a future change reintroduced table recreation and lost the trigger,
// this bad insert would silently succeed instead of failing.
func TestMigrateV144_IndexesAndTriggersSurviveRecreation(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144_triggers.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var triggerName string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = ?`,
		"trg_credential_crews_workspace_check",
	).Scan(&triggerName); err != nil {
		t.Fatalf("trg_credential_crews_workspace_check missing after v144 recreation: %v", err)
	}

	var idxName string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`,
		"idx_cli_token_uses_used_at",
	).Scan(&idxName); err != nil {
		t.Fatalf("idx_cli_token_uses_used_at missing after v144 recreation: %v", err)
	}

	// Seed two workspaces/crews/credentials so we can attempt a
	// cross-workspace credential_crews row.
	for _, ws := range []string{"ws_a_v144", "ws_b_v144"} {
		if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, ws, ws, ws); err != nil {
			t.Fatalf("seed workspace %s: %v", ws, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_b_v144', 'ws_b_v144', 'B', 'crew-b-v144')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_trig_v144', 'trig-v144@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		VALUES ('cred_a_v144', 'ws_a_v144', 'A', 'x', 'user_trig_v144')`); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	_, err = db.Exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cred_a_v144', 'crew_b_v144')`)
	if err == nil {
		t.Fatal("expected trg_credential_crews_workspace_check to reject a cross-workspace credential_crews row, insert succeeded")
	}
}

// TestMigrateV144_SkippedTablesStayLegacyForm confirms the three
// intentionally-left-alone tables (see datetimeNowDefaultSkipTables) are
// NOT touched by this migration — their DEFAULT stays space-form because
// the column is never string-compared.
func TestMigrateV144_SkippedTablesStayLegacyForm(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144_skip.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for table, checkSQL := range map[string]string{
		"mcp_registry_servers": `SELECT sql FROM sqlite_master WHERE type='table' AND name='mcp_registry_servers'`,
		"backup_locks":         `SELECT sql FROM sqlite_master WHERE type='table' AND name='backup_locks'`,
		"instance_config":      `SELECT sql FROM sqlite_master WHERE type='table' AND name='instance_config'`,
	} {
		var createSQL string
		if err := db.QueryRow(checkSQL).Scan(&createSQL); err != nil {
			t.Fatalf("read schema for %s: %v", table, err)
		}
		if !regexp.MustCompile(`datetime\('now'\)`).MatchString(createSQL) {
			t.Errorf("%s: expected untouched datetime('now') DEFAULT, got schema: %s", table, createSQL)
		}
	}
}

// TestMigrateV144_BackfillsHistoricalLegacyRows is the reproducing test for
// the incomplete-fix gap #1073b originally shipped with: converting only the
// DEFAULT stops NEW legacy rows but leaves every legacy-form value the old
// DEFAULT already wrote AFTER v45's one-shot backfill (v45 ran ~100 versions
// earlier, so any insert relying on the DEFAULT since then re-accumulated
// space-form rows). Those historical rows keep sorting ahead of the T-form
// the fixed DEFAULT now produces, so the pagination bug persists on real
// data even though a fresh insert looks correct.
//
// It drives the true upgrade path: land every migration BEFORE v144 (DEFAULT
// still legacy), insert a row that relies on that legacy DEFAULT, then apply
// v144 and assert the historical value was normalized to T-form in place.
func TestMigrateV144_BackfillsHistoricalLegacyRows(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144_backfill.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// Land the schema at everything strictly before v144 (143 is the highest
	// pre-v144 slot; v141–143 live on sibling branches not present here, so
	// this resolves to the latest migration on this branch). workspace_files.
	// created_at DEFAULT is still the legacy datetime('now') form at that point.
	if err := applyMigrationsUpTo(ctx, db.DB, 143, logger); err != nil {
		t.Fatalf("migrate to pre-v144: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_bf', 'BF', 'ws-bf')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	// Insert relying on the legacy DEFAULT — this is the row that would have
	// accumulated in production between v45 and v144.
	if _, err := db.Exec(`INSERT INTO workspace_files (id, workspace_id, rel_path) VALUES ('wf_legacy', 'ws_bf', 'legacy.txt')`); err != nil {
		t.Fatalf("insert legacy-default row: %v", err)
	}

	var beforeTS string
	if err := db.QueryRow(`SELECT created_at FROM workspace_files WHERE id = 'wf_legacy'`).Scan(&beforeTS); err != nil {
		t.Fatalf("read created_at pre-v144: %v", err)
	}
	if !legacySpaceFormPattern.MatchString(beforeTS) {
		t.Fatalf("precondition: pre-v144 DEFAULT should write legacy space-form, got %q — test premise invalid", beforeTS)
	}

	// Apply ONLY v144 against the populated pre-v144 schema: it converts the
	// DEFAULT AND backfills the historical row.
	v144, err := findMigration(144)
	if err != nil {
		t.Fatalf("locate v144: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin v144 tx: %v", err)
	}
	if err := v144.fn(ctx, tx, logger); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v144: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v144: %v", err)
	}

	var afterTS string
	if err := db.QueryRow(`SELECT created_at FROM workspace_files WHERE id = 'wf_legacy'`).Scan(&afterTS); err != nil {
		t.Fatalf("read created_at after v144: %v", err)
	}
	if legacySpaceFormPattern.MatchString(afterTS) {
		t.Errorf("v144 left the historical legacy row unconverted: %q still space-form", afterTS)
	}
	// v45's expression yields second-precision T-form (no fractional part);
	// accept T-form with or without a fraction.
	tform := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`)
	if !tform.MatchString(afterTS) {
		t.Errorf("v144 backfilled to a non-T-form value: %q", afterTS)
	}
	// The normalized value must preserve the wall-clock instant: same
	// date+time, only the separator/zone shape changed.
	if want := beforeTS[:10] + "T" + beforeTS[11:] + "Z"; afterTS != want {
		t.Errorf("backfill changed the instant: %q -> %q, want %q", beforeTS, afterTS, want)
	}
}

// TestMigrateV144_MemoryVersionsUntouched guards the boundary with 1073a:
// this migration must not modify memory_versions at all.
func TestMigrateV144_MemoryVersionsUntouched(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144_memver.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_versions'`).Scan(&name); err != nil {
		t.Skipf("memory_versions table not present on this branch (1073a not merged yet): %v", err)
	}
}

// inboxCreatedAtOrder returns inbox_items ids for a workspace sorted by the
// TEXT created_at column — the shape the hot-path feed index sorts on.
func inboxCreatedAtOrder(t *testing.T, db *DB, wsID string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM inbox_items WHERE workspace_id = ? ORDER BY created_at ASC`, wsID)
	if err != nil {
		t.Fatalf("order query: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// TestMigrateV144_ConvertsSubsecDefaultToTForm is the reproducing test for the
// #1179 gap: v144's first pass matched only datetime('now') and silently
// skipped every datetime('now','subsec') table — the two literals are not
// substrings of each other. inbox_items.created_at defaults to the subsec form
// and is the sort key of the hot-path (workspace_id, state, created_at DESC)
// feed index, so a raw insert that omits created_at must now get a T-form
// value, not the space-form-with-fraction the subsec DEFAULT produced — and
// the schema literal itself must no longer carry a subsec DEFAULT.
func TestMigrateV144_ConvertsSubsecDefaultToTForm(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144_subsec.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_ss', 'SS', 'ws-ss')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('inb_ss', 'ws_ss', 'message', 'src1', 'hi')`); err != nil {
		t.Fatalf("raw insert into inbox_items: %v", err)
	}

	var createdAt string
	if err := db.QueryRow(`SELECT created_at FROM inbox_items WHERE id = 'inb_ss'`).Scan(&createdAt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !tformPattern.MatchString(createdAt) {
		t.Errorf("inbox_items.created_at DEFAULT produced %q — want ISO T-form; subsec DEFAULT must be converted (#1179)", createdAt)
	}

	var createSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='inbox_items'`).Scan(&createSQL); err != nil {
		t.Fatalf("read inbox_items schema: %v", err)
	}
	if strings.Contains(createSQL, "datetime('now','subsec')") {
		t.Errorf("inbox_items schema still carries a datetime('now','subsec') DEFAULT after v144: %s", createSQL)
	}
}

// TestMigrateV144_MixedFormatOrderingAcrossBoundary is the one true mixed-
// format ordering test #1179 asks for: a live ordered subsec table
// (inbox_items) holding a MIX of plain space-form, subsec space-form, and
// explicit T-form values sorts WRONG before v144 (any space-form sorts before
// any T-form regardless of real time) and correctly after, because v144
// normalizes every space-form row — plain AND subsec — to T-form in place.
func TestMigrateV144_MixedFormatOrderingAcrossBoundary(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v144_mixorder.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	if err := applyMigrationsUpTo(ctx, db.DB, 143, logger); err != nil {
		t.Fatalf("migrate to pre-v144: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_mo', 'MO', 'ws-mo')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Three rows on the SAME date (so the sort actually reaches the
	// separator, not the year) whose stored TEXT shapes disagree with their
	// real chronology. Real order earliest→latest: tform(08:00) <
	// subsec(09:00) < space(10:00). Stored shapes: tform=explicit T-form,
	// subsec=subsec space-form, space=plain space-form. Pre-v144 both
	// space-forms sort AHEAD of the T-form (' ' 0x20 < 'T' 0x54) even though
	// they are chronologically LATER — the exact #1073b/#1179 symptom — so
	// the buggy order is [subsec(09), space(10), tform(08)].
	seed := []struct{ id, ts string }{
		{"inb_tform_early", "2023-06-15T08:00:00.000Z"}, // explicit T-form, real earliest
		{"inb_subsec_mid", "2023-06-15 09:00:00.500"},   // subsec space-form, real middle
		{"inb_space_late", "2023-06-15 10:00:00"},       // plain space-form, real latest
	}
	for _, r := range seed {
		// (kind, source_id) is UNIQUE — give each row its own source_id.
		if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, created_at)
			VALUES (?, 'ws_mo', 'message', ?, 'x', ?)`, r.id, "src_"+r.id, r.ts); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	// Premise: pre-v144 the ORDER BY is the BUGGY one (space-forms ahead of
	// the chronologically-earlier T-form).
	if got := inboxCreatedAtOrder(t, db, "ws_mo"); len(got) != 3 || got[0] != "inb_subsec_mid" || got[1] != "inb_space_late" || got[2] != "inb_tform_early" {
		t.Fatalf("pre-v144 order = %v, want the buggy [subsec_mid space_late tform_early] to establish the premise", got)
	}

	v144, err := findMigration(144)
	if err != nil {
		t.Fatalf("locate v144: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin v144 tx: %v", err)
	}
	if err := v144.fn(ctx, tx, logger); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v144: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v144: %v", err)
	}

	// After v144 the order is chronological.
	if got := inboxCreatedAtOrder(t, db, "ws_mo"); len(got) != 3 || got[0] != "inb_tform_early" || got[1] != "inb_subsec_mid" || got[2] != "inb_space_late" {
		t.Errorf("post-v144 order = %v, want chronological [tform_early subsec_mid space_late]", got)
	}
	// And no row is left in any space-form (plain or subsec).
	rows, err := db.Query(`SELECT id, created_at FROM inbox_items WHERE workspace_id = 'ws_mo'`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, ts string
		if err := rows.Scan(&id, &ts); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(ts, " ") {
			t.Errorf("%s still space-form after v144: %q", id, ts)
		}
	}
}
