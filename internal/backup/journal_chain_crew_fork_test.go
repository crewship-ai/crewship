package backup_test

// #5578: a crew bundle carries only the journal entries whose crew_id is that
// crew, but their seq numbers come from the WHOLE workspace's chain. The
// extract therefore has gaps wherever a sibling crew (or the workspace itself)
// wrote an entry. The forked-restore re-sign keeps any gap no signed
// checkpoint covers, on purpose, so a workspace fork inherits its source's
// integrity. For a crew extract that turned every expected gap into a reported
// mid-chain delete: `journal verify` on the --as-crew copy failed at once.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
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

// The crew-extract renumbering must be decided from the encrypted payload,
// not from the manifest. The manifest is a plaintext tar member, so anyone
// able to write the bundle file can flip its scope to "crew" without the
// passphrase. A workspace bundle relabelled that way must keep its real gap
// (a deleted row) and fail verify after an --as-crew restore, exactly as it
// does on the source.
func TestJournalChain_ManifestScopeFlipDoesNotLaunderGaps(t *testing.T) {
	ctx := context.Background()

	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)
	seedJournalChain(t, db, workspaceID, 6)
	if _, err := db.ExecContext(ctx, `DELETE FROM journal_entries WHERE workspace_id = ? AND seq = 3`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if res, err := journal.VerifyChain(ctx, db, workspaceID); err != nil || res.OK {
		t.Fatalf("fixture: the source must fail verify after the delete (ok=%v err=%v)", res.OK, err)
	}

	const passphrase = "scope-flip-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID, OutputDir: t.TempDir(),
		Actor: actor, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Relabel the bundle as a crew bundle of c_alpha, touching only the
	// plaintext manifest.
	src, err := os.Open(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	m, sealed, closeBundle, err := backup.ReadBundleStream(src)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(sealed)
	if err != nil {
		t.Fatal(err)
	}
	_ = closeBundle()
	_ = src.Close()
	m.Scope = backup.ScopeCrew
	var alpha []backup.CrewSummary
	for _, c := range m.Contents.Crews {
		if c.ID == "c_alpha" {
			alpha = append(alpha, c)
		}
	}
	if len(alpha) != 1 {
		t.Fatalf("fixture: manifest crews %+v carry no c_alpha", m.Contents.Crews)
	}
	m.Contents.Crews = alpha
	var out bytes.Buffer
	if err := backup.WriteBundleStream(&out, m, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	flipped := strings.TrimSuffix(created.Path, ".tar.zst") + "-flipped.tar.zst"
	if err := os.WriteFile(flipped, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path: flipped, Passphrase: passphrase, Actor: actor, AsCrew: "alpha-flipped",
	})
	if err != nil {
		// Refusing the relabelled bundle outright is also acceptable.
		t.Logf("relabelled bundle refused: %v", err)
		return
	}
	verify, err := journal.VerifyChain(ctx, db, res.RestoredWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if verify.OK {
		t.Fatalf("a workspace bundle relabelled as crew scope came back with a clean chain: the deleted seq 3 was laundered by renumbering")
	}
	var payloadJSON string
	if err := db.QueryRowContext(ctx, `SELECT payload FROM journal_entries WHERE workspace_id = ? AND entry_type = ?`,
		res.RestoredWorkspaceID, string(journal.EntryBackupChainResigned)).Scan(&payloadJSON); err == nil &&
		strings.Contains(payloadJSON, "crew_extract_renumbered") {
		t.Errorf("notice claims a crew-extract renumbering for a relabelled workspace bundle: %s", payloadJSON)
	}
}
