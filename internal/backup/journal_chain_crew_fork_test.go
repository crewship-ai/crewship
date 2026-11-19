package backup_test

// #5578: a crew bundle carries only the journal entries whose crew_id is that
// crew, but their seq numbers come from the WHOLE workspace's chain. The
// extract therefore has gaps wherever a sibling crew (or the workspace itself)
// wrote an entry. The forked-restore re-sign keeps any gap no signed
// checkpoint covers, on purpose, so a workspace fork inherits its source's
// integrity. For a crew extract that turned every expected gap into a reported
// mid-chain delete: `journal verify` on the --as-crew copy failed at once.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestJournalChain_SurvivesCrewForkedRestore(t *testing.T) {
	ctx := context.Background()

	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)
	// Six entries alternating c_beta, c_alpha, ... so c_alpha owns seq 2, 4, 6:
	// a crew extract with a gap before every row.
	seedJournalChain(t, db, workspaceID, 6)
	mustVerify(t, db, workspaceID, "source (pre-backup)")

	var crewEntries int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND crew_id = 'c_alpha' AND seq > 0`,
		workspaceID).Scan(&crewEntries); err != nil {
		t.Fatal(err)
	}
	if crewEntries != 3 {
		t.Fatalf("fixture: c_alpha owns %d chained entries, want 3", crewEntries)
	}

	const passphrase = "crew-fork-chain-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:      backup.ScopeCrew,
		CrewID:     "c_alpha",
		OutputDir:  t.TempDir(),
		Actor:      actor,
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup crew: %v", err)
	}
	res, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path: created.Path, Passphrase: passphrase, Actor: actor, AsCrew: "alpha-copy",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-crew: %v", err)
	}
	fork := res.RestoredWorkspaceID
	if fork == "" || fork == workspaceID {
		t.Fatalf("--as-crew did not land in its own workspace (got %q, source %q)", fork, workspaceID)
	}

	// Every extracted row lands; none is lost to a seq collision.
	var landed int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND entry_type != ?`,
		fork, string(journal.EntryBackupChainResigned)).Scan(&landed); err != nil {
		t.Fatal(err)
	}
	if landed != crewEntries {
		t.Errorf("crew copy holds %d restored journal entries, want the extract's %d", landed, crewEntries)
	}

	mustVerify(t, db, workspaceID, "source (post crew fork)")
	mustVerify(t, db, fork, "crew copy (--as-crew)")

	// The renumbering is not silent: the re-sign notice says the chain is a
	// crew extract and which source seqs it came from.
	var payload string
	if err := db.QueryRowContext(ctx,
		`SELECT payload FROM journal_entries WHERE workspace_id = ? AND entry_type = ?`,
		fork, string(journal.EntryBackupChainResigned)).Scan(&payload); err != nil {
		t.Fatalf("read re-sign notice: %v", err)
	}
	var notice map[string]any
	if err := json.Unmarshal([]byte(payload), &notice); err != nil {
		t.Fatalf("notice payload: %v", err)
	}
	if notice["crew_extract_renumbered"] != true {
		t.Errorf("re-sign notice does not record the crew-extract renumbering: %s", payload)
	}
	if notice["source_seq_first"] != float64(2) || notice["source_seq_last"] != float64(6) {
		t.Errorf("re-sign notice source seq range = %v..%v, want 2..6: %s",
			notice["source_seq_first"], notice["source_seq_last"], payload)
	}
}
