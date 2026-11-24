package backup_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/database"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// A fork (--as-workspace / --as-crew → RemapIDs) must give each waitpoint a
// fresh public id and must NOT inherit the source's callback capability
// (#1910, #2274): the digest is dropped, so no URL that completes the source's
// gate completes the copy, and restoring the fork beside its source cannot
// collide on the partial UNIQUE index over callback_token_hash. The Inbox row
// that points at the waitpoint follows the new id.
func TestWaitpointForkDropsCallbackDigestAndRemintsID(t *testing.T) {
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)

	const (
		sourceID = pipeline.WaitpointIDPrefix + "0123456789abcdef0123456789abcdef"
		digest   = "sh1:synthetic-waitpoint-callback-digest"
	)
	for _, stmt := range []string{
		`INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,prompt,invoking_crew_id,timeout_at,callback_token_hash)
		 VALUES('` + sourceID + `','ws_e2e_1','wp-fork-run','approve','approval','Ship it?','c_alpha','2099-01-01T00:00:00Z','` + digest + `')`,
		`INSERT INTO inbox_items(id,workspace_id,kind,source_id,title)
		 VALUES('ibx_ws_e2e_1_waitpoint_` + sourceID + `','ws_e2e_1','waitpoint','` + sourceID + `','Ship it?')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	dump, err := backup.DumpWorkspace(t.Context(), db, ws)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(dump.Tables["pipeline_waitpoints"]); n != 1 {
		t.Fatalf("dump carries %d waitpoint rows, want 1", n)
	}
	if got := dump.Tables["pipeline_waitpoints"][0]["callback_token_hash"]; got != digest {
		t.Fatalf("fixture digest not dumped: %v", got)
	}

	if err = backup.RemapIDs(t.Context(), db, dump); err != nil {
		t.Fatal(err)
	}
	row := dump.Tables["pipeline_waitpoints"][0]
	forkID, _ := row["token"].(string)
	if !strings.HasPrefix(forkID, pipeline.WaitpointIDPrefix) || forkID == sourceID {
		t.Fatalf("fork waitpoint id %q: want a fresh %q id distinct from the source", forkID, pipeline.WaitpointIDPrefix)
	}
	if got := row["callback_token_hash"]; got != nil {
		t.Fatalf("fork inherited the source's callback digest: %v", got)
	}
	var inboxSource string
	for _, item := range dump.Tables["inbox_items"] {
		if item["kind"] == "waitpoint" {
			inboxSource, _ = item["source_id"].(string)
		}
	}
	if inboxSource != forkID {
		t.Fatalf("fork inbox source_id %q does not follow the new waitpoint id %q", inboxSource, forkID)
	}

	// Land the fork beside its source: the partial UNIQUE index on the
	// digest must not fire, and the source keeps its capability untouched.
	fork := dump.Tables["workspaces"][0]["id"].(string)
	dump.Tables["workspaces"][0]["slug"] = "waitpoint-fork-copy"
	if err = backup.RestoreDump(t.Context(), db, dump); err != nil {
		t.Fatalf("restore fork beside source: %v", err)
	}
	var restoredID string
	var restoredHash sql.NullString
	if err = db.QueryRow(`SELECT token,callback_token_hash FROM pipeline_waitpoints WHERE workspace_id=?`, fork).Scan(&restoredID, &restoredHash); err != nil {
		t.Fatal(err)
	}
	if restoredID != forkID || restoredHash.Valid {
		t.Fatalf("restored fork waitpoint %q hash=%v, want %q with NULL hash", restoredID, restoredHash, forkID)
	}
	var sourceHash string
	if err = db.QueryRow(`SELECT callback_token_hash FROM pipeline_waitpoints WHERE token=?`, sourceID).Scan(&sourceHash); err != nil || sourceHash != digest {
		t.Fatalf("source waitpoint lost its digest %q %v", sourceHash, err)
	}
	var forkInbox int
	if err = db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id=? AND kind='waitpoint' AND source_id=?`, fork, forkID).Scan(&forkInbox); err != nil || forkInbox != 1 {
		t.Fatalf("restored fork inbox rows pointing at %q: %d %v", forkID, forkInbox, err)
	}
	assertNoFKViolations(t, db, "waitpoint fork")
}

func TestRestoreLegacyWaitpointDoesNotRecreateAnUpgradedGate(t *testing.T) {
	ctx := t.Context()
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	const old = "0123456789abcdef0123456789abcdef"
	const oldInbox = "ibx_ws_e2e_1_waitpoint_" + old
	if _, err := db.Exec(`INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,timeout_at)
		VALUES(?,?,'restored-run','approve','approval','2099-01-01T00:00:00Z')`, old, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items(id,workspace_id,kind,source_id,title) VALUES(?,?,'waitpoint',?,'Approve')`, oldInbox, ws, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_item_reads(inbox_item_id,user_id) VALUES(?,'u_admin')`, oldInbox); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, db, ws)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(dump)
	if err != nil || strings.Contains(string(raw), old) {
		t.Fatal("workspace export retained a legacy waitpoint identity")
	}
	if err := backup.RestoreDump(ctx, db, dump); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM inbox_items WHERE workspace_id=? AND kind='waitpoint'", ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("partial destination has %d cards (%v)", n, err)
	}
	store := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(store.Close)
	var current string
	if err := db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE workspace_id=?`, ws).Scan(&current); err != nil {
		t.Fatal(err)
	}
	secret, _, err := store.MintCallbackToken(ctx, ws, current, "u_admin")
	if err != nil {
		t.Fatal(err)
	}
	// A bundle from before the independent chat source ID could otherwise
	// also create a second pending waitpoint when inserted beside the first.
	delete(dump.Tables["pipeline_waitpoints"][0], "chat_source_id")
	if err := backup.RestoreDump(ctx, db, dump); err != nil {
		t.Fatal(err)
	}
	if id, _, err := store.ResolveCallbackToken(ctx, secret); err != nil || id != current {
		t.Fatalf("additive restore replaced the live callback: id=%q err=%v", id, err)
	}
	var waitpoints, cards, reads int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM pipeline_waitpoints WHERE workspace_id=?),
		(SELECT count(*) FROM inbox_items WHERE workspace_id=? AND kind='waitpoint'),
		(SELECT count(*) FROM inbox_item_reads WHERE inbox_item_id=? OR inbox_item_id=?)`, ws, ws, oldInbox, "ibx_ws_e2e_1_waitpoint_"+current).Scan(&waitpoints, &cards, &reads); err != nil {
		t.Fatal(err)
	}
	if waitpoints != 1 || cards != 1 || reads != 1 {
		t.Fatalf("legacy restore added duplicate rows: waitpoints=%d inbox=%d reads=%d", waitpoints, cards, reads)
	}
	assertNoFKViolations(t, db, "legacy waitpoint restore")
}

func TestRestoreWaitpointInboxRetainsHireAndAutonomySources(t *testing.T) {
	ctx := t.Context()
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	for _, kind := range []struct{ id, title string }{{"hire-agent", "Hire"}, {"crew-autonomy", "Autonomy"}} {
		if _, err := source.Exec(`INSERT INTO inbox_items(id,workspace_id,kind,source_id,title)
			VALUES(?,?,'waitpoint',?,?)`, "ibx_"+ws+"_waitpoint_"+kind.id, ws, kind.id, kind.title); err != nil {
			t.Fatal(err)
		}
	}
	dump, err := backup.DumpWorkspace(ctx, source, ws)
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDB(t)
	if err := backup.RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := target.QueryRow(`SELECT count(*) FROM inbox_items WHERE workspace_id=? AND kind='waitpoint'
		AND source_id IN ('hire-agent','crew-autonomy')`, ws).Scan(&n); err != nil || n != 2 {
		t.Fatalf("unrelated Inbox sources restored=%d err=%v, want 2", n, err)
	}
}

func TestWaitpointInstanceRecoveryDuringConversion(t *testing.T) {
	f := newInstanceFixture(t)
	const ws = "ws_e2e_1"
	const old = "b7e26627108a4149a8d17dcc40dbe7bb"
	if _, err := f.db.Exec(`INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,timeout_at)
		VALUES(?,?,'old-run','gate','approval','2099-01-01'),('wp_current',?,'current-run','gate','approval','2099-01-01')`, old, ws, ws); err != nil {
		t.Fatal(err)
	}
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "restored")
	if _, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pipeline_waitpoints WHERE workspace_id=? AND token IN (?, 'wp_current') AND callback_token_hash IS NULL", ws, pipeline.LegacyWaitpointPublicID(ws, old)).Scan(&n); err != nil || n != 2 {
		t.Fatalf("recovered waitpoints=%d (%v)", n, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM pipeline_waitpoints WHERE token=?", old).Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy identifier remained after startup: %d (%v)", n, err)
	}
}
