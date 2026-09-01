package backup_test

// The broader question behind #2260: are OTHER tables in BackupTables silently
// in the same position as missions, and simply have not been hit yet because
// nobody's bundle happened to carry a row for them?
//
// The other fork tests each seed the handful of tables they care about, so
// "forked restore works" has only ever been asserted over the tables somebody
// wrote a fixture for. This one synthesises a row into EVERY BackupTables entry
// it can, forks the bundle back into the same instance, and then asserts two
// things about the result:
//
//	1. PRAGMA foreign_key_check is empty — no orphan survived the fork.
//	2. Every remappable table gained exactly as many rows as the bundle carried
//	   for it — i.e. no row was swallowed by RestoreDump's INSERT OR IGNORE.
//
// (2) is the assertion that would have caught #2260 directly: the missions row
// collided on UNIQUE(trace_id), OR IGNORE dropped it without a word, and only
// its orphaned mission_activity child made any noise.
//
// Tables the generator cannot synthesise a row for (exclusive-arc CHECKs,
// constraints it cannot infer) are reported by name rather than passed over in
// silence, so a shrinking coverage set is visible in the test output.

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// nonRemappablePKTablesForTest mirrors backup.nonRemappablePKTables, which is
// package-private and cannot be reached from this external test package. These
// tables are globally namespaced: a fork deliberately does NOT regenerate their
// primary keys, so their bundle rows are expected to collide with the target's
// existing rows and be dropped by INSERT OR IGNORE. Row-count parity does not
// apply to them — which is why they are named here rather than the assertion
// being loosened for everyone.
var nonRemappablePKTablesForTest = map[string]bool{
	"skills": true,
	"users":  true,
}

// forkExtraRows returns the rows a forked restore writes ON TOP of the ones the
// bundle carries, per table. Anything not returned here must match the bundle
// exactly.
//
// Computed from the bundle rather than hard-coded, because the one entry is
// conditional: ensureRestoringUserMembership (#1215) grants the admin who ran
// the restore membership on the brand-new workspace — without it the fork is
// unreachable to the person who just made it — but it no-ops when the bundle
// already carries that membership. Which of those two the row-per-table fixture
// produces depends on which user its generated workspace_members row happens to
// point at, so the expectation mirrors the production rule instead of guessing.
//
// journal_entries is deliberately NOT here. rechainForkedJournal (#2226)
// appends a chain re-sign notice, but only for a workspace whose chain it
// actually rewrote, and this fixture's entries are unchained (seq 0) so nothing
// is re-signed and no notice is written. TestJournalChain_SurvivesForkedRestore
// pins the notice against a real chain; if chained entries are ever added to
// this fixture, this is the comment that explains the off-by-one.
func forkExtraRows(dump *backup.DBDump, actorUserID string) map[string]int {
	out := map[string]int{}
	for _, r := range dump.Tables["workspace_members"] {
		if r["user_id"] == actorUserID {
			return out // already a member; the restore adds nothing
		}
	}
	out["workspace_members"] = 1
	return out
}

// knownForkDrops names tables whose rows a forked restore still SILENTLY LOSES,
// with the constraint that eats them. Every one is the #2260 shape — a unique
// key the fork does not renegotiate, and INSERT OR IGNORE dropping the row
// rather than failing — but none is the one-line fix #2260 itself was:
//
//	token / token_hash pairs (workspace_invitations, port_exposures,
//	pipeline_webhooks, page_public_tokens, page_webhooks) cannot be handled by
//	minting a fresh column value in isolation. The cleartext token and its
//	digest have to be minted TOGETHER, through the same primitive the auth
//	layer uses to check them (#1888 hashed these at rest precisely so the
//	column is not a credential store), or the fork gets a row whose hash — the
//	actual lookup key — matches no token anybody holds.
//
//	inbox_items UNIQUE(kind, source_id) and message_feedback
//	UNIQUE(message_id, user_id, signal) have no token to regenerate at all:
//	their unique keys are built from columns nothing in the bundle remaps
//	(source_id is an opaque handle; `messages` is not even in BackupTables).
//	Fixing those means scoping the constraint or the key, not minting a value.
//
// Filed as #2274. Pinned here rather than skipped: when one is fixed, this test
// fails on the entry that is no longer true and the entry gets deleted — that
// is how the list shrinks instead of quietly rotting.
var knownForkDrops = map[string]string{
	"workspace_invitations": "UNIQUE token (v01) — instance-wide, not regenerated on fork",
	"port_exposures":        "UNIQUE token + UNIQUE token_hash (#1888) — instance-wide",
	"pipeline_webhooks":     "UNIQUE token + UNIQUE token_hash (v82, #1888) — instance-wide",
	"page_public_tokens":    "UNIQUE token_hash — instance-wide by design (a shared hash is a cross-page read)",
	"page_webhooks":         "UNIQUE token_hash — instance-wide by design",
	"inbox_items":           "UNIQUE(kind, source_id) — neither column is remapped",
	"message_feedback":      "UNIQUE(message_id, user_id, signal) — messages are not in BackupTables, so message_id is never remapped",
}

// checkInListRe pulls the first allowed literal out of a column-level
// `CHECK(col IN ('a','b'))`, which is how this schema spells its enums. Good
// enough to satisfy the constraint; not a SQL parser, and not trying to be.
var checkInListRe = regexp.MustCompile(`(?is)check\s*\(\s*"?([a-z_][a-z0-9_]*)"?\s+in\s*\(([^)]*)\)`)

type genColumn struct {
	name     string
	declType string
	notNull  bool
	hasDflt  bool
	pk       bool
}

// tableCreateSQL returns the CREATE TABLE text SQLite stored for table, or ""
// when the table does not exist.
func tableCreateSQL(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var sqlText sql.NullString
	err := db.QueryRowContext(context.Background(),
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&sqlText)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("read schema of %s: %v", table, err)
	}
	return sqlText.String
}

func tableGenColumns(t *testing.T, db *sql.DB, table string) []genColumn {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []genColumn
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		out = append(out, genColumn{
			name: name, declType: ctype,
			notNull: notnull == 1, hasDflt: dflt.Valid, pk: pk > 0,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return out
}

type genFK struct{ from, refTable, refColumn string }

func tableGenFKs(t *testing.T, db *sql.DB, table string) map[string]genFK {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		t.Fatalf("foreign_key_list(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]genFK{}
	for rows.Next() {
		var id, seq int
		var refTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign_key_list(%s): %v", table, err)
		}
		if to == "" {
			to = "id"
		}
		out[from] = genFK{from: from, refTable: refTable, refColumn: to}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign_key_list(%s): %v", table, err)
	}
	return out
}

// checkAllowedValues maps column → first literal permitted by a column-level
// CHECK(... IN (...)) in the table's DDL.
func checkAllowedValues(createSQL string) map[string]string {
	out := map[string]string{}
	for _, m := range checkInListRe.FindAllStringSubmatch(createSQL, -1) {
		col := strings.ToLower(m[1])
		first := strings.TrimSpace(strings.Split(m[2], ",")[0])
		first = strings.Trim(first, `'"`)
		if first != "" {
			if _, seen := out[col]; !seen {
				out[col] = first
			}
		}
	}
	return out
}

// resolveFKValue finds a value in the parent table that this workspace's bundle
// will also carry, so the synthesised child row is not an orphan the DUMP drops
// (which would make the row-count assertion below vacuous rather than wrong).
func resolveFKValue(t *testing.T, db *sql.DB, fk genFK, workspaceID string) (any, bool) {
	t.Helper()
	ctx := context.Background()
	queries := []struct {
		q    string
		args []any
	}{
		{fmt.Sprintf(`SELECT %s FROM %s WHERE workspace_id = ? LIMIT 1`, fk.refColumn, fk.refTable), []any{workspaceID}},
		{fmt.Sprintf(`SELECT %s FROM %s LIMIT 1`, fk.refColumn, fk.refTable), nil},
	}
	for _, cand := range queries {
		var v any
		if err := db.QueryRowContext(ctx, cand.q, cand.args...).Scan(&v); err == nil && v != nil {
			return v, true
		}
	}
	return nil, false
}

// synthValue invents a value for a NOT NULL column with no default and no
// foreign key. The shapes are deliberately boring — the point is a row that
// satisfies the constraints, not a realistic one.
func synthValue(table string, c genColumn, allowed map[string]string, createSQL string) any {
	if v, ok := allowed[strings.ToLower(c.name)]; ok {
		return v
	}
	dt := strings.ToUpper(c.declType)
	switch {
	case strings.Contains(dt, "INT"):
		return int64(1)
	case strings.Contains(dt, "REAL"), strings.Contains(dt, "FLOA"), strings.Contains(dt, "DOUB"), strings.Contains(dt, "NUM"):
		return 1.0
	case strings.Contains(dt, "BLOB"):
		return []byte("x")
	}
	lower := strings.ToLower(c.name)
	// json_valid() guards are common on the payload-ish columns; an empty
	// object satisfies every one of them.
	if strings.Contains(strings.ToLower(createSQL), "json_valid("+lower+")") ||
		strings.Contains(strings.ToLower(createSQL), `json_valid("`+lower+`")`) {
		return "{}"
	}
	switch {
	case strings.HasSuffix(lower, "_at"), lower == "ts":
		return time.Now().UTC().Format(time.RFC3339)
	case strings.Contains(lower, "json"), strings.Contains(lower, "payload"),
		strings.Contains(lower, "config"), strings.Contains(lower, "refs"),
		strings.Contains(lower, "details"), strings.Contains(lower, "metadata"):
		return "{}"
	}
	return "gen-" + table + "-" + lower
}

// genColumnOverrides pins `table.column` values the generator cannot infer from
// the schema alone. Two kinds live here, and nothing else belongs:
//
//   - a nil value means "leave this column out of the INSERT". Needed for the
//     exclusive-arc CHECKs, where the schema permits exactly one of a group of
//     nullable FKs to be set and the generator would happily set them all.
//   - a non-nil value pins content whose SHAPE is not expressible as a
//     constraint — `{}` satisfies json_valid() but not the Go type a reader
//     unmarshals the column into.
//
// Each entry is a place the generator gave up, so keep the list readable: a
// growing one is a signal, not an inconvenience.
var genColumnOverrides = map[string]any{
	// removed_json is unmarshalled into []journal.RemovedEntry, so the
	// generic json_valid-satisfying "{}" makes rechainForkedJournal fail.
	"journal_chain_checkpoints.removed_json": "[]",
	// pages: CHECK((owner_user_id IS NOT NULL) <> (owner_crew_id IS NOT NULL)).
	"pages.owner_crew_id": nil,
	// attachments: exactly one of mission_id / comment_id / chat_id, and
	// CHECK(length(sha256) = 64).
	"attachments.comment_id": nil,
	"attachments.chat_id":    nil,
	"attachments.sha256":     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	// credential_bindings: scope decides which of crew_id / agent_id may be set.
	"credential_bindings.scope":    "WORKSPACE",
	"credential_bindings.crew_id":  nil,
	"credential_bindings.agent_id": nil,
	// credential_fields: is_secret picks which of value / encrypted_value is set.
	"credential_fields.is_secret":       int64(0),
	"credential_fields.value":           "gen-plaintext",
	"credential_fields.encrypted_value": nil,
	// onboarding_proposals: PENDING requires every applied_* column to be NULL.
	"onboarding_proposals.status":          "PENDING",
	"onboarding_proposals.applied_crew_id": nil,
}

// seedOneBackupTable attempts to synthesise a single row into table. Reports
// why it could not, so the caller can retry (a parent may be seeded later in
// BackupTables order) and ultimately report the shortfall.
func seedOneBackupTable(t *testing.T, db *sql.DB, table, workspaceID string) error {
	t.Helper()
	createSQL := tableCreateSQL(t, db, table)
	if createSQL == "" {
		return fmt.Errorf("absent from schema")
	}
	cols := tableGenColumns(t, db, table)
	fks := tableGenFKs(t, db, table)
	allowed := checkAllowedValues(createSQL)

	var names []string
	var args []any
	for _, c := range cols {
		lower := strings.ToLower(c.name)
		if override, ok := genColumnOverrides[table+"."+strings.ToLower(c.name)]; ok {
			if override == nil {
				continue
			}
			names = append(names, c.name)
			args = append(args, override)
			continue
		}
		switch {
		case lower == "id" && c.pk:
			names = append(names, c.name)
			args = append(args, "gen_"+table)
		case lower == "workspace_id":
			names = append(names, c.name)
			args = append(args, workspaceID)
		default:
			if fk, ok := fks[c.name]; ok {
				v, found := resolveFKValue(t, db, fk, workspaceID)
				if !found {
					if c.notNull && !c.hasDflt {
						return fmt.Errorf("%s → %s.%s has no candidate row",
							c.name, fk.refTable, fk.refColumn)
					}
					continue // nullable FK: leave it NULL
				}
				names = append(names, c.name)
				args = append(args, v)
				continue
			}
			if c.notNull && !c.hasDflt {
				names = append(names, c.name)
				args = append(args, synthValue(table, c, allowed, createSQL))
			}
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no columns to write")
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	stmt := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		table, strings.Join(names, ","), placeholders)
	if _, err := db.ExecContext(context.Background(), stmt, args...); err != nil {
		return err
	}
	return nil
}

// seedRowPerBackupTable synthesises one row into every BackupTables entry it
// can. Returns the tables it seeded and the ones it could not, so the caller
// can report coverage.
//
// Runs repeatedly until a pass seeds nothing new: BackupTables is ordered for
// FK-safe RESTORE, which is not the same as an order in which every parent
// happens to precede every child of a synthetic fixture (milestones is listed
// before projects, for one). A fixed point is simpler and more robust than
// hand-maintaining a second ordering.
func seedRowPerBackupTable(t *testing.T, db *sql.DB, workspaceID string) (seeded, skipped []string) {
	t.Helper()

	done := map[string]bool{}
	failures := map[string]string{}
	for {
		progress := false
		for _, table := range backup.BackupTables {
			if done[table] {
				continue
			}
			if err := seedOneBackupTable(t, db, table, workspaceID); err != nil {
				failures[table] = err.Error()
				continue
			}
			done[table] = true
			delete(failures, table)
			seeded = append(seeded, table)
			progress = true
		}
		if !progress {
			break
		}
	}
	for _, table := range backup.BackupTables {
		if reason, ok := failures[table]; ok {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", table, reason))
		}
	}
	return seeded, skipped
}

// countRows returns the current row count of every table in BackupTables that
// exists in the schema.
func countBackupTableRows(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range backup.BackupTables {
		if tableCreateSQL(t, db, table) == "" {
			continue
		}
		var n int
		if err := db.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// TestForkedRestore_EveryBackupTable answers #2260's "worth answering while
// investigating": fork a bundle carrying at least one row for as much of
// BackupTables as can be synthesised, into the same instance, and require both
// referential integrity and row-count parity afterwards.
func TestForkedRestore_EveryBackupTable(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedLiveMission(t, source, workspaceID)
	seeded, skipped := seedRowPerBackupTable(t, source, workspaceID)

	// Coverage is part of the result: a future schema change that makes the
	// generator give up on a table should be visible, not silent.
	sort.Strings(skipped)
	t.Logf("row-per-table fixture: seeded %d table(s); could not seed %d:\n  %s",
		len(seeded), len(skipped), strings.Join(skipped, "\n  "))
	if len(seeded) < 40 {
		t.Fatalf("only %d of %d BackupTables entries got a row — the generator has "+
			"stopped covering enough of the schema for this test to mean anything",
			len(seeded), len(backup.BackupTables))
	}

	// What the bundle actually carries, per table. Taken from the same public
	// dump the backup uses, so the expectation below is the bundle's own truth
	// rather than a guess about the fixture.
	dump, err := backup.DumpWorkspace(ctx, source, workspaceID)
	if err != nil {
		t.Fatalf("DumpWorkspace: %v", err)
	}

	before := countBackupTableRows(t, source)

	const passphrase = "fork-every-table-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       actor,
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	res, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:        created.Path,
		Passphrase:  passphrase,
		Actor:       actor,
		AsWorkspace: "e2e-ws-every-table-fork",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	if res.RestoredWorkspaceID == "" || res.RestoredWorkspaceID == workspaceID {
		t.Fatalf("--as-workspace did not fork the workspace (got %q, source %q)",
			res.RestoredWorkspaceID, workspaceID)
	}

	assertNoFKViolations(t, source, "after every-table --as-workspace fork")

	after := countBackupTableRows(t, source)
	extra := forkExtraRows(dump, actor.UserID)
	var lost []string
	for _, table := range backup.BackupTables {
		want := len(dump.Tables[table])
		if want == 0 {
			continue
		}
		if nonRemappablePKTablesForTest[table] {
			// Globally namespaced: the bundle rows are meant to collide
			// with the target's own and be ignored. Assert THAT, so a
			// change of policy here fails loudly too.
			if got := after[table] - before[table]; got != 0 {
				t.Errorf("%s is non-remappable but the fork added %d row(s); "+
					"a globally namespaced table must pass through", table, got)
			}
			continue
		}
		if reason, known := knownForkDrops[table]; known {
			// A pinned gap: the bundle rows are known to be eaten today.
			// Assert the count that documents it, so fixing one of these
			// fails here and the entry can be removed.
			if got := after[table] - before[table]; got != 0 {
				t.Errorf("%s is listed in knownForkDrops (%s) but the fork added %d row(s) — "+
					"if that gap is fixed, delete the knownForkDrops entry", table, reason, got)
			}
			continue
		}
		want += extra[table]
		if got := after[table] - before[table]; got != want {
			lost = append(lost, fmt.Sprintf(
				"%s: bundle carried %d row(s), fork added %d", table, len(dump.Tables[table]), got))
		}
	}
	if len(lost) > 0 {
		t.Errorf("a forked restore silently dropped rows (INSERT OR IGNORE swallowed a "+
			"non-PK UNIQUE collision — the #2260 class of bug):\n  %s",
			strings.Join(lost, "\n  "))
	}
}
