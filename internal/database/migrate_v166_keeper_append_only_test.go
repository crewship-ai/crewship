package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// v166HasColumn reports whether table has the named column. Deliberately not
// sharing a generic `columnInfo` helper: the credential-lease branch adds one of
// those in the same package, and two identically-named package-local helpers
// would collide the moment both branches merge.
func v166HasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid          int
			name, ctype  string
			notNull, pk  int
			defaultValue *string
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows table_info(%s): %v", table, err)
	}
	return false
}

// seedKeeperRequestFixture stages the FK targets a keeper_requests row needs.
func seedKeeperRequestFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w1')`)
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('u1','a@example.com')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cw1','ws1','C','c1')`)
	mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('ag1','cw1','ws1','A','a1')`)
	mustExec(t, db, `INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, created_by)
		VALUES ('cr1','ws1','TOK','enc','SECRET','NONE','u1')`)
}

// TestMigrate_V166_KeeperEventsTableShape asserts the ledger table exists with the
// per-request uniqueness that makes a missing or duplicated transition detectable.
func TestMigrate_V166_KeeperEventsTableShape(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='keeper_request_events'`).Scan(&n); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if n != 1 {
		t.Fatal("keeper_request_events missing after v166")
	}
	for _, col := range []string{"request_id", "workspace_id", "seq", "state", "actor_type", "recorded_at"} {
		if !v166HasColumn(t, db.DB, "keeper_request_events", col) {
			t.Errorf("keeper_request_events missing %s column", col)
		}
	}
}

// TestMigrate_V166_KeeperEventsRejectUpdate is the load-bearing assertion behind
// "keeper decisions are append-only": immutability must be enforced by the
// DATABASE, not by every future caller remembering to insert instead of update.
func TestMigrate_V166_KeeperEventsRejectUpdate(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedKeeperRequestFixture(t, db.DB)

	mustExec(t, db.DB, `INSERT INTO keeper_request_events
		(id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES ('e1','kr1','ws1',1,'PENDING','keeper','2026-07-25T10:00:00Z')`)

	_, err := db.Exec(`UPDATE keeper_request_events SET state = 'ALLOW' WHERE id = 'e1'`)
	if err == nil {
		t.Fatal("UPDATE on keeper_request_events succeeded — the ledger is not append-only")
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("expected an append-only abort, got: %v", err)
	}

	var state string
	if err := db.QueryRow(`SELECT state FROM keeper_request_events WHERE id='e1'`).Scan(&state); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if state != "PENDING" {
		t.Errorf("state = %q after a rejected UPDATE, want PENDING", state)
	}
}

// TestMigrate_V166_KeeperEventsDuplicateSeqRejected: a monotonic per-request seq
// is what makes a missing transition visible, so two rows must never share one.
func TestMigrate_V166_KeeperEventsDuplicateSeqRejected(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedKeeperRequestFixture(t, db.DB)

	mustExec(t, db.DB, `INSERT INTO keeper_request_events
		(id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES ('e1','kr1','ws1',1,'PENDING','keeper','2026-07-25T10:00:00Z')`)
	if _, err := db.Exec(`INSERT INTO keeper_request_events
		(id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES ('e2','kr1','ws1',1,'ALLOW','keeper','2026-07-25T10:00:01Z')`); err == nil {
		t.Fatal("duplicate (request_id, seq) was accepted")
	}
}

// TestMigrate_V166_BackfillsExistingDecisions lands the pre-v166 schema, writes
// keeper_requests rows the old (in-place-UPDATE) way, then applies ONLY v166 —
// so the backfill has genuine legacy data to reconstruct from, the way a real
// upgrade does.
//
// It asserts the migration recovers what it legitimately can (a PENDING at
// created_at plus the decision at decided_at) and invents nothing for a request
// that is still pending.
func TestMigrate_V166_BackfillsExistingDecisions(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := applyMigrationsUpTo(ctx, db, 165, logger); err != nil {
		t.Fatalf("migrate to 165: %v", err)
	}

	// Written the pre-v166 way: PENDING then UPDATEd in place to the decision, so
	// only the end state survives in keeper_requests.
	mustExec(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('ag1','ws1','A','a1')`)
	mustExec(t, db, `INSERT INTO keeper_requests
		(id, requesting_agent_id, requesting_crew_id, credential_id, intent, decision, reason, risk_score, exit_code, created_at, decided_at, request_type)
		VALUES ('kr_decided','ag1','cw1','cr1','deploy','ALLOW','ok',2,0,'2026-07-25T10:00:00Z','2026-07-25T10:00:05Z','execute')`)
	mustExec(t, db, `INSERT INTO keeper_requests
		(id, requesting_agent_id, requesting_crew_id, credential_id, intent, decision, created_at, request_type)
		VALUES ('kr_pending','ag1','cw1','cr1','deploy','PENDING','2026-07-25T10:01:00Z','access')`)

	m, err := findMigration(166)
	if err != nil {
		t.Fatalf("find v166: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v166: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The decided request yields both transitions, in order.
	type ev struct {
		seq   int
		state string
		at    string
	}
	rows, err := db.QueryContext(ctx,
		`SELECT seq, state, recorded_at FROM keeper_request_events WHERE request_id='kr_decided' ORDER BY seq`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	var got []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.seq, &e.state, &e.at); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		got = append(got, e)
	}
	rows.Close()

	want := []ev{
		{1, "PENDING", "2026-07-25T10:00:00Z"},
		{2, "ALLOW", "2026-07-25T10:00:05Z"},
	}
	if len(got) != len(want) {
		t.Fatalf("decided request produced %d events (%+v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A still-pending request gets exactly one event — there is no decision to
	// invent, and inventing one would be a fabricated audit record.
	var pendingOnly int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM keeper_request_events WHERE request_id='kr_pending'`).Scan(&pendingOnly); err != nil {
		t.Fatalf("count pending-only: %v", err)
	}
	if pendingOnly != 1 {
		t.Fatalf("pending request produced %d events, want 1", pendingOnly)
	}

	// Workspace is resolved from the requesting agent, so the ledger is readable
	// per-tenant even though keeper_requests has no workspace_id column.
	var ws sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT workspace_id FROM keeper_request_events WHERE request_id='kr_decided' AND seq=1`).Scan(&ws); err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if ws.String != "ws1" {
		t.Errorf("workspace_id = %q, want ws1", ws.String)
	}

	// Idempotent: the deterministic ids mean a restore replay cannot double the
	// ledger.
	for i := 0; i < 2; i++ {
		if _, err := db.ExecContext(ctx, backfillOnly(m.sql)); err != nil {
			t.Fatalf("replay backfill: %v", err)
		}
	}
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM keeper_request_events`).Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if total != 3 {
		t.Fatalf("ledger rows after replay = %d, want 3 (backfill must be idempotent)", total)
	}
}

// backfillOnly extracts just the INSERT OR IGNORE statements from a migration's
// SQL, so a test can replay the data-seeding half without re-running the DDL
// (which is not idempotent for ADD COLUMN).
func backfillOnly(migrationSQL string) string {
	var out []string
	for _, stmt := range strings.Split(migrationSQL, ";") {
		if strings.Contains(strings.ToUpper(stmt), "INSERT OR IGNORE") {
			out = append(out, strings.TrimSpace(stmt))
		}
	}
	return strings.Join(out, ";\n") + ";"
}

// TestMigrate_V166_PriorityAtEmitSeeded asserts the immutable chain column exists
// and is seeded from the CURRENT priority. That seeding is what keeps chains
// emitted before this migration verifying: their stored entry_hash was computed
// over that same value, and the emit-time value is unrecoverable for a row that
// was already edited.
func TestMigrate_V166_PriorityAtEmitSeeded(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := applyMigrationsUpTo(ctx, db, 165, logger); err != nil {
		t.Fatalf("migrate to 165: %v", err)
	}
	// A legacy row an operator had pinned, and one left at the default.
	mustExec(t, db, `INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, actor_type, summary, priority)
		VALUES ('je_pinned','ws1','2026-07-25T10:00:00Z','run.started','info','agent','s','pin')`)
	mustExec(t, db, `INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, actor_type, summary)
		VALUES ('je_plain','ws1','2026-07-25T10:00:01Z','run.started','info','agent','s')`)

	m, err := findMigration(166)
	if err != nil {
		t.Fatalf("find v166: %v", err)
	}
	if _, err := db.ExecContext(ctx, m.sql); err != nil {
		t.Fatalf("apply v166: %v", err)
	}

	if !v166HasColumn(t, db, "journal_entries", "priority_at_emit") {
		t.Fatal("journal_entries missing priority_at_emit after v166")
	}
	for _, tc := range []struct{ id, want string }{
		{"je_pinned", "pin"},
		{"je_plain", "normal"},
	} {
		var atEmit string
		if err := db.QueryRowContext(ctx,
			`SELECT priority_at_emit FROM journal_entries WHERE id=?`, tc.id).Scan(&atEmit); err != nil {
			t.Fatalf("read %s: %v", tc.id, err)
		}
		if atEmit != tc.want {
			t.Errorf("%s priority_at_emit = %q, want %q", tc.id, atEmit, tc.want)
		}
	}

	// The edit ledger must stay EMPTY. Because priority_at_emit is seeded from the
	// current value, reconciliation ("the live value must be reachable from
	// priority_at_emit through the recorded changes") already holds with zero
	// recorded changes.
	//
	// Seeding a synthetic 'normal' -> 'pin' row would BREAK it: the first edit's
	// previous_priority would not match priority_at_emit, so every legacy pinned
	// entry would verify as tampered. See TestMigrate_V166_LegacyPinnedRowReconciles.
	for _, id := range []string{"je_pinned", "je_plain"} {
		var ledger int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM journal_entry_priorities WHERE entry_id=?`, id).Scan(&ledger); err != nil {
			t.Fatalf("count ledger (%s): %v", id, err)
		}
		if ledger != 0 {
			t.Errorf("%s has %d backfilled ledger rows, want 0 — a synthetic edit would break reconciliation", id, ledger)
		}
	}
}

// TestMigrate_V166_LegacyPinnedRowReconciles is the regression for the trap in
// the backfill: an entry an operator had ALREADY pinned before this migration
// must reconcile cleanly afterwards.
//
// The failure mode is subtle and total. priority_at_emit is seeded from the
// current priority, so an intuitive "also seed the edit ledger so the historical
// change is recorded" backfill produces previous_priority='normal' against
// priority_at_emit='pin' — the chain of changes then does not start where it must,
// and EVERY legacy pinned entry verifies as tampered. That would ship a
// tamper-evidence feature whose first act is to declare existing installs
// compromised.
func TestMigrate_V166_LegacyPinnedRowReconciles(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := applyMigrationsUpTo(ctx, db, 165, logger); err != nil {
		t.Fatalf("migrate to 165: %v", err)
	}
	mustExec(t, db, `INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, actor_type, summary, priority)
		VALUES ('je_legacy_pin','ws1','2026-07-25T10:00:00Z','run.started','info','agent','s','permanent')`)

	m, err := findMigration(166)
	if err != nil {
		t.Fatalf("find v166: %v", err)
	}
	if _, err := db.ExecContext(ctx, m.sql); err != nil {
		t.Fatalf("apply v166: %v", err)
	}

	// Reconciliation input as the verifier reads it.
	var atEmit, live string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(priority_at_emit,''), COALESCE(priority,'normal')
		   FROM journal_entries WHERE id='je_legacy_pin'`).Scan(&atEmit, &live); err != nil {
		t.Fatalf("read row: %v", err)
	}
	var edits int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entry_priorities WHERE entry_id='je_legacy_pin'`).Scan(&edits); err != nil {
		t.Fatalf("count edits: %v", err)
	}

	// With no recorded edits, reconciliation requires live == priority_at_emit.
	// Any backfilled edit row would have to chain from priority_at_emit, which a
	// synthetic 'normal' start cannot do.
	if edits != 0 {
		t.Fatalf("backfill wrote %d edit rows; a synthetic edit cannot chain from priority_at_emit=%q", edits, atEmit)
	}
	if live != atEmit {
		t.Fatalf("live priority %q != priority_at_emit %q — a legacy pinned entry would verify as tampered", live, atEmit)
	}
}

// TestMigrate_V166_PriorityLedgerRejectsUpdate: the priority-change ledger is the
// only thing distinguishing "an operator pinned this" from "someone flipped the
// column in the DB", so it must be immutable too.
func TestMigrate_V166_PriorityLedgerRejectsUpdate(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w1')`)
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, entry_type, severity, actor_type, summary)
		VALUES ('je1','ws1','run.started','info','agent','s')`)
	mustExec(t, db.DB, `INSERT INTO journal_entry_priorities
		(id, entry_id, workspace_id, seq, previous_priority, priority, set_at)
		VALUES ('p1','je1','ws1',1,'normal','pin','2026-07-25T10:00:00Z')`)

	_, err := db.Exec(`UPDATE journal_entry_priorities SET priority = 'normal' WHERE id = 'p1'`)
	if err == nil {
		t.Fatal("UPDATE on journal_entry_priorities succeeded — the ledger is not append-only")
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("expected an append-only abort, got: %v", err)
	}
}

// TestMigrate_V166_RestoreBackfillSeedsPriorityAtEmit covers the restore path the
// migration itself cannot reach: rows re-inserted from a pre-v166 bundle land with
// priority_at_emit NULL, and the migration has already run so it will never fix
// them.
//
// A NULL anchor is not harmless. VerifyChain reads
// COALESCE(priority_at_emit, priority, 'normal'), so the anchor becomes the LIVE
// priority — which moves the moment an operator edits it, and the recorded change
// then no longer starts where the anchor sits. Verification would report tampering
// on a legitimate edit.
func TestMigrate_V166_RestoreBackfillSeedsPriorityAtEmit(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w1')`)
	// Simulate restored rows: present, but with the v166 column left NULL the way
	// a pre-v166 bundle's INSERT leaves it.
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, actor_type, summary, priority, priority_at_emit)
		VALUES ('je_restored_pin','ws1','2026-07-25T10:00:00Z','run.started','info','agent','s','pin',NULL)`)
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, actor_type, summary, priority_at_emit)
		VALUES ('je_restored_plain','ws1','2026-07-25T10:00:01Z','run.started','info','agent','s',NULL)`)
	// An already-correct row the hook must NOT touch.
	mustExec(t, db.DB, `INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, actor_type, summary, priority, priority_at_emit)
		VALUES ('je_intact','ws1','2026-07-25T10:00:02Z','run.started','info','agent','s','normal','permanent')`)

	hook := RestoreBackfillFor(166)
	if hook == nil {
		t.Fatal("v166 has no restoreBackfill hook — restored pre-v166 rows would keep a NULL chain anchor")
	}

	// Run it TWICE: the contract requires idempotence, because a restore that
	// fails on a later hook is re-run from the start against already-seeded rows.
	for i := 0; i < 2; i++ {
		tx, err := db.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := hook(ctx, tx, logger); err != nil {
			_ = tx.Rollback()
			t.Fatalf("hook run %d: %v", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit run %d: %v", i+1, err)
		}
	}

	for _, tc := range []struct{ id, want string }{
		{"je_restored_pin", "pin"},
		{"je_restored_plain", "normal"},
		// Untouched: the hook only fills NULLs, so it cannot clobber a value the
		// migration (or the emit path) already set.
		{"je_intact", "permanent"},
	} {
		var atEmit string
		if err := db.QueryRow(
			`SELECT COALESCE(priority_at_emit,'') FROM journal_entries WHERE id=?`, tc.id).Scan(&atEmit); err != nil {
			t.Fatalf("read %s: %v", tc.id, err)
		}
		if atEmit != tc.want {
			t.Errorf("%s priority_at_emit = %q, want %q", tc.id, atEmit, tc.want)
		}
	}
}
