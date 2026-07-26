package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// The upgrade path is what production actually runs, and until this test it was
// the one thing the suite never exercised.
//
// Every other migration test starts from an empty database, and stage — the
// promotability gate — wipes its volumes and reseeds from scratch on every
// deploy ("the deterministic, migration-clean starting point"). So the whole
// pipeline proved "a fresh install of this commit is healthy" and said nothing
// about whether an existing database survives. That is the opposite of the
// question you ask before promoting: production sits many versions back and
// upgrading it is precisely a migration over real rows.
//
// Seven migrations in the v140–v166 range TRANSFORM data rather than just
// adding schema, and on zero rows a data transform is a no-op that passes for
// free:
//
//	v140 encrypt_webhook_secrets          v152 journal_hash_chain
//	v141 memory_versions_tsformat_backfill v159 run_step_outputs
//	v144 datetime_now_default_tform        v161 notification_prefs
//	v148 backfill_network_mode_restricted
//
// This lands a schema at v139, seeds every table those seven touch, then runs
// the PRODUCTION Migrate() to HEAD and asserts each transform actually
// happened — and that nothing was lost on the way.
func TestUpgradePath_V139WithDataMigratesToHead(t *testing.T) {
	// v140 and v152 both key off ENCRYPTION_KEY. v152 is the one that matters:
	// journal.ChainKeyFromEnv() does not error on a missing key, it derives from
	// "" — so an upgrade run without a key chains the whole history under a
	// null seed and every entry fails verification forever afterwards. The test
	// must therefore run WITH a key, or it would assert nothing about the real
	// upgrade and would quietly bless the broken one.
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8)) // 64 hex chars

	dir := t.TempDir()
	db, err := sql.Open("sqlite", dir+"/upgrade.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := applyMigrationsUpTo(ctx, db, 139, quiet); err != nil {
		t.Fatalf("land schema at v139: %v", err)
	}

	// ── seed a workspace's worth of legacy rows ───────────────────────────────
	const (
		wsID   = "ws_upgrade"
		userID = "usr_upgrade"
		crewID = "crew_upgrade"
		pipeID = "pipe_upgrade"
		runID  = "run_upgrade"
		whID   = "pwh_upgrade"
		plain  = "super-secret-webhook-signing-key"
	)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %.60s...: %v", q, err)
		}
	}
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "Upgrade", "upgrade")
	exec(`INSERT INTO users (id, email) VALUES (?,?)`, userID, "upgrade@example.test")
	// network_mode 'free' is the legacy default v148 must flip to 'restricted'.
	exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode) VALUES (?,?,?,?,'free')`,
		crewID, wsID, "Crew", "crew")
	exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
	      VALUES (?,?,?,?,?,?)`, pipeID, wsID, "routine", "Routine", `{"steps":[]}`, "hash")
	// v159 fans this JSON blob out into pipeline_run_step_outputs.
	exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, step_outputs_json)
	      VALUES (?,?,?,?,'completed',?,?)`,
		runID, wsID, pipeID, "routine", tsformat.Format(time.Now().UTC()),
		`{"parse":"parsed output","notify":"sent"}`)
	// v140 encrypts this in place.
	exec(`INSERT INTO pipeline_webhooks (id, workspace_id, name, target_pipeline_id, token, signing_secret)
	      VALUES (?,?,?,?,?,?)`, whID, wsID, "hook", pipeID, "tok_upgrade", plain)
	// v161 rewrites this table's CHECK constraint and adds NOT NULL columns —
	// on an empty table that is trivially safe, which is the point of seeding it.
	exec(`INSERT INTO notification_channels (id, workspace_id, type, config_json)
	      VALUES (?,?,?,?)`, "nch_upgrade", wsID, "webhook", `{"url":"https://example.test/hook"}`)
	// v141 normalizes legacy written_at shapes to fixed-width tsformat.
	exec(`INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, written_at, payload_ref)
	      VALUES (?,?,?,?,?,?,?,?)`,
		"mv_upgrade", wsID, "notes.md", "agent", "deadbeef", 12,
		"2026-05-01 09:15:00", "blob/deadbeef")
	// v144 rewrites the space-form datetime('now') literals these carry.
	exec(`INSERT INTO workspace_files (id, workspace_id, rel_path, size_bytes, created_at, updated_at)
	      VALUES (?,?,?,?,?,?)`,
		"wf_upgrade", wsID, "docs/readme.md", 42, "2026-05-01 09:15:00", "2026-05-01 09:15:00")
	// v152 hash-chains these. More than one, in two workspaces' worth of
	// ordering, so a chain that only works for a single row is not enough.
	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	for i, e := range []struct{ id, typ, summary string }{
		{"j_up_1", "agent.started", "first"},
		{"j_up_2", "agent.finished", "second"},
		{"j_up_3", "credential.granted", "third"},
	} {
		exec(`INSERT INTO journal_entries (id, workspace_id, crew_id, ts, entry_type, actor_type, summary, payload)
		      VALUES (?,?,?,?,?,'system',?,'{}')`,
			e.id, wsID, crewID, tsformat.Format(base.Add(time.Duration(i)*time.Minute)), e.typ, e.summary)
	}

	countBefore := map[string]int{}
	for _, tbl := range []string{"workspaces", "crews", "pipelines", "pipeline_runs",
		"pipeline_webhooks", "notification_channels", "memory_versions", "workspace_files", "journal_entries"} {
		countBefore[tbl] = countRows(t, db, tbl)
	}

	// ── the thing under test: the production apply loop, over real rows ───────
	if err := Migrate(ctx, db, quiet); err != nil {
		t.Fatalf("upgrade v139 -> HEAD over populated tables: %v", err)
	}

	var head int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM _migrations`).Scan(&head); err != nil {
		t.Fatalf("read head version: %v", err)
	}
	if head != migrations[len(migrations)-1].version {
		t.Fatalf("upgrade stopped at v%d, want v%d", head, migrations[len(migrations)-1].version)
	}

	// ── nothing may be lost ───────────────────────────────────────────────────
	for tbl, want := range countBefore {
		if got := countRows(t, db, tbl); got != want {
			t.Errorf("%s: %d rows before the upgrade, %d after — the upgrade dropped data", tbl, want, got)
		}
	}

	// ── v148: legacy 'free' crews are fenced ──────────────────────────────────
	var mode string
	if err := db.QueryRowContext(ctx, `SELECT network_mode FROM crews WHERE id = ?`, crewID).Scan(&mode); err != nil {
		t.Fatalf("read crew network_mode: %v", err)
	}
	if mode != "restricted" {
		t.Errorf("v148: crew network_mode = %q, want restricted — a legacy crew kept open egress through the upgrade", mode)
	}

	// ── v140: the webhook secret is no longer readable as plaintext ───────────
	var stored sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT signing_secret FROM pipeline_webhooks WHERE id = ?`, whID).Scan(&stored); err != nil {
		t.Fatalf("read signing_secret: %v", err)
	}
	if stored.String == plain {
		t.Error("v140: signing_secret is still the plaintext value — the encrypt-at-rest backfill did not run over existing rows")
	}
	if stored.String == "" {
		t.Error("v140: signing_secret was emptied by the upgrade — encryption must not lose the value")
	}

	// ── v141: legacy timestamp shape normalized ───────────────────────────────
	var writtenAt string
	if err := db.QueryRowContext(ctx, `SELECT written_at FROM memory_versions WHERE id = 'mv_upgrade'`).Scan(&writtenAt); err != nil {
		t.Fatalf("read memory_versions.written_at: %v", err)
	}
	// Assert the SHAPE parses, not its length: tsformat.Layout is 35 chars as a
	// layout but a UTC value renders "Z" rather than "+00:00", so a length
	// comparison against the layout is wrong by six.
	if strings.Contains(writtenAt, " ") {
		t.Errorf("v141: written_at = %q still carries the legacy space-form shape", writtenAt)
	}
	if _, err := time.Parse(tsformat.Layout, writtenAt); err != nil {
		t.Errorf("v141: written_at = %q does not parse as tsformat: %v", writtenAt, err)
	}

	// ── v144: the space-form literal is gone ──────────────────────────────────
	var createdAt string
	if err := db.QueryRowContext(ctx, `SELECT created_at FROM workspace_files WHERE id = 'wf_upgrade'`).Scan(&createdAt); err != nil {
		t.Fatalf("read workspace_files.created_at: %v", err)
	}
	if strings.Contains(createdAt, " ") {
		t.Errorf("v144: created_at = %q still carries the legacy space-form shape", createdAt)
	}

	// ── v159: the JSON blob was fanned out, not just left behind ──────────────
	var stepRows int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_run_step_outputs WHERE run_id = ?`, runID).Scan(&stepRows); err != nil {
		t.Fatalf("read pipeline_run_step_outputs: %v", err)
	}
	if stepRows != 2 {
		t.Errorf("v159: %d step-output rows for a run with 2 steps in step_outputs_json — the backfill did not fan out existing runs", stepRows)
	}

	// ── v152: the chain covers the pre-existing rows AND verifies ─────────────
	//
	// This is the assertion the whole test exists for. A hash-chain backfilled
	// over historical rows is only worth something if it verifies afterwards;
	// an empty-table test can never tell you that, because there is nothing to
	// chain.
	var unchained int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE entry_hash IS NULL OR entry_hash = ''`).Scan(&unchained); err != nil {
		t.Fatalf("count unchained journal rows: %v", err)
	}
	if unchained != 0 {
		t.Errorf("v152: %d journal rows have no entry_hash — pre-existing entries were left outside the chain", unchained)
	}
	res, err := journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("v152: VerifyChain after upgrade: %v", err)
	}
	if !res.OK {
		t.Errorf("v152: the chain does not verify after the upgrade (break at seq %d: %s) — tamper-evidence is dead on every row that predates the migration",
			res.BrokenSeq, res.Reason)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	// #nosec G202 -- table names come from a fixed literal list in this test.
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// Why the upgrade must never run without ENCRYPTION_KEY — demonstrated, not
// asserted from reasoning.
//
// v152 backfills the journal hash-chain from journal.ChainKeyFromEnv(), which
// does NOT error on a missing key: internal/journal/verify.go derives from
// os.Getenv("ENCRYPTION_KEY"), i.e. the empty string. The upgrade succeeds, and
// the resulting chain is SELF-CONSISTENT — it verifies fine as long as you keep
// verifying with the same empty key. That is what makes it dangerous: the
// damage is invisible at the moment it happens, and to any test that runs
// entirely keyless (the test above passes its chain assertion without a key for
// exactly this reason).
//
// It surfaces the first time the real key shows up, which in production is the
// next `crewship start`. By then the migration is recorded as applied, so it
// cannot re-run, and every entry predating the upgrade is permanently
// unverifiable — the tamper-evidence of the whole history, gone silently.
//
// This is the failure mode `openLocalDB` now refuses to walk into by declining
// to upgrade an existing schema. If that guard is ever removed, the reasoning
// behind it lives here.
func TestUpgradePath_KeylessUpgradePoisonsTheJournalChain(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", dir+"/keyless.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := applyMigrationsUpTo(ctx, db, 139, quiet); err != nil {
		t.Fatalf("land schema at v139: %v", err)
	}
	const wsID = "ws_keyless"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "Keyless", "keyless"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	base := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{"j_kl_1", "j_kl_2", "j_kl_3"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO journal_entries (id, workspace_id, ts, entry_type, actor_type, summary, payload)
			 VALUES (?,?,?,'agent.started','system','historical entry','{}')`,
			id, wsID, tsformat.Format(base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("seed journal: %v", err)
		}
	}

	// The doctor/telemetry path: migrate with no key loaded.
	t.Setenv("ENCRYPTION_KEY", "")
	if err := Migrate(ctx, db, quiet); err != nil {
		t.Fatalf("keyless upgrade: %v", err)
	}

	// It looks fine. This is the trap.
	res, err := journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("VerifyChain (keyless): %v", err)
	}
	if !res.OK {
		t.Fatalf("expected the keyless chain to verify against the same empty key — if this fails the hazard has changed shape and this test needs rewriting")
	}

	// Now the server starts and loads the real key, exactly as cmd_start does.
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	res, err = journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("VerifyChain (real key): %v", err)
	}
	if res.OK {
		t.Skip("the chain now survives a key change — ChainKeyFromEnv may no longer seed from the key, which would make the keyless-upgrade hazard obsolete. Re-check openLocalDB's guard before deleting it.")
	}
	t.Logf("confirmed: after a keyless upgrade the chain breaks at seq %d (%s) once the real key loads, and v152 cannot re-run",
		res.BrokenSeq, res.Reason)
}
