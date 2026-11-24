package backup_test

import (
	"database/sql"
	"strings"
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
	store := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(store.Close)
	var current string
	if err := db.QueryRow(`SELECT token FROM pipeline_waitpoints WHERE workspace_id=?`, ws).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MintCallbackToken(ctx, ws, current); err != nil {
		t.Fatal(err)
	}
	if err := backup.RestoreDump(ctx, db, dump); err != nil {
		t.Fatal(err)
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
