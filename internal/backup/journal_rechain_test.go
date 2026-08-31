package backup_test

// A forked restore re-signs the journal chain (see
// journal_chain_fork_restore_test.go for the integrity property). These tests
// cover the two things that MUST ride with that re-sign and are not implied by
// "the chain verifies":
//
//  1. The fork says it was re-signed. A chain that verifies clean while
//     staying silent asserts unbroken provenance back to the source's genesis,
//     which is a stronger claim than a fork can support — the source's
//     signatures covered ids that no longer exist. So a forked restore emits
//     journal.EntryBackupChainResigned, and a PLAIN restore, which remaps
//     nothing and re-signs nothing, must not.
//
//  2. The fork does not write into the SOURCE workspace's audit state. Before
//     the fix, journal_chain_checkpoints.workspace_id carried no foreign key,
//     so the remap regenerated the row's primary key but left it pointing at
//     the source — one duplicate checkpoint per forked restore, accumulating
//     in a table whose whole job is to be a trustworthy record of deletions.
//
//  3. Re-signing does not LAUNDER. Moving checkpoints under the fork's
//     workspace id means re-MACing them, and re-MACing unconditionally would
//     turn a forgery the source rejected into a signature this installation
//     vouches for.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

// resignEntries returns the re-sign notices recorded for a workspace, newest
// last, with the payload decoded.
func resignNotices(t *testing.T, db *sql.DB, workspaceID string) []map[string]any {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT payload, seq, severity, summary FROM journal_entries
		 WHERE workspace_id = ? AND entry_type = ? ORDER BY seq ASC`,
		workspaceID, string(journal.EntryBackupChainResigned))
	if err != nil {
		t.Fatalf("query re-sign notices: %v", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var payload, severity, summary string
		var seq int64
		if err := rows.Scan(&payload, &seq, &severity, &summary); err != nil {
			t.Fatalf("scan re-sign notice: %v", err)
		}
		decoded := map[string]any{}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("re-sign notice payload is not JSON (%q): %v", payload, err)
		}
		decoded["_seq"] = seq
		decoded["_severity"] = severity
		decoded["_summary"] = summary
		out = append(out, decoded)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate re-sign notices: %v", err)
	}
	return out
}

// TestForkedRestore_RecordsThatTheChainWasResigned is the positive half: the
// fork carries exactly one notice, it names what it forked from, and — the
// part that matters for tamper-evidence — the notice is itself INSIDE the
// chain it describes, so it cannot be dropped without breaking verification.
func TestForkedRestore_RecordsThatTheChainWasResigned(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 4)

	const passphrase = "resign-notice-pass-123"
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
		AsWorkspace: "e2e-ws-resign",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}

	notices := resignNotices(t, source, res.RestoredWorkspaceID)
	if len(notices) != 1 {
		t.Fatalf("forked workspace has %d %s entries, want exactly 1",
			len(notices), journal.EntryBackupChainResigned)
	}
	n := notices[0]

	// It must name the source it forked from. Without that the entry says
	// "something was re-signed" and an auditor has no way back to the
	// bundle that produced it.
	if got := n["source_workspace_id"]; got != workspaceID {
		t.Errorf("re-sign notice source_workspace_id = %v, want %s", got, workspaceID)
	}
	if got, _ := n["entries_resigned"].(float64); int(got) != 4 {
		t.Errorf("re-sign notice entries_resigned = %v, want 4", n["entries_resigned"])
	}
	if n["_severity"] != string(journal.SeverityNotice) {
		t.Errorf("re-sign notice severity = %v, want %s", n["_severity"], journal.SeverityNotice)
	}

	// The notice sits at the TAIL of the re-signed chain (seq 5 after the
	// four restored rows), which is what puts it under the same HMAC as
	// everything else. Deleting it would leave an uncheckpointed gap.
	if seq, _ := n["_seq"].(int64); seq != 5 {
		t.Errorf("re-sign notice seq = %v, want 5 (the tail of the re-signed chain)", n["_seq"])
	}

	// And the source must be untouched: the fork records its own genesis,
	// it does not annotate the workspace it copied.
	if got := resignNotices(t, source, workspaceID); len(got) != 0 {
		t.Errorf("source workspace %s gained %d re-sign entries; a fork must not write into the workspace it forked from",
			workspaceID, len(got))
	}

	if res.JournalEntriesResigned != 4 {
		t.Errorf("RestoreResult.JournalEntriesResigned = %d, want 4", res.JournalEntriesResigned)
	}
}

// TestPlainRestore_DoesNotClaimAResign is the control. A plain restore remaps
// nothing, so the bundle's own signatures are still valid and there is no new
// genesis to declare. Emitting the notice anyway would be a false claim in the
// audit trail — the opposite failure to the one #2226 reported, and just as
// bad.
func TestPlainRestore_DoesNotClaimAResign(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 4)

	const passphrase = "plain-no-resign-pass-123"
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

	target := openMigratedDB(t)
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       created.Path,
		Passphrase: passphrase,
		Actor:      actor,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	if got := resignNotices(t, target, workspaceID); len(got) != 0 {
		t.Errorf("plain restore emitted %d %s entries; nothing was remapped, so nothing was re-signed",
			len(got), journal.EntryBackupChainResigned)
	}
	if res.JournalEntriesResigned != 0 || res.JournalCheckpointsResigned != 0 {
		t.Errorf("plain restore reported %d entries / %d checkpoints re-signed, want 0/0",
			res.JournalEntriesResigned, res.JournalCheckpointsResigned)
	}

	var total int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ?`, workspaceID).Scan(&total); err != nil {
		t.Fatalf("count restored entries: %v", err)
	}
	if total != 4 {
		t.Fatalf("plain restore landed %d journal entries, want the 4 the bundle carried", total)
	}
}

// TestForkedRestore_DoesNotDuplicateSourceCheckpoints pins the write
// amplification half of #2226. journal_chain_checkpoints.workspace_id has no
// REFERENCES clause, so the remap's FK pass never saw it while pass 1 still
// regenerated the row's primary key: every forked restore INSERTed a fresh
// checkpoint row pointing at the SOURCE workspace. The source's checkpoint set
// must be exactly what it was before the fork ran.
func TestForkedRestore_DoesNotDuplicateSourceCheckpoints(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 5)

	// Compact seq 3 out, recording the signed checkpoint that keeps the
	// gap from reading as tampering.
	key := journal.ChainKeyFromEnv()
	var removedHash string
	if err := source.QueryRowContext(ctx,
		`SELECT entry_hash FROM journal_entries WHERE workspace_id = ? AND seq = 3`,
		workspaceID).Scan(&removedHash); err != nil {
		t.Fatalf("read seq 3 hash: %v", err)
	}
	if _, err := source.ExecContext(ctx,
		`DELETE FROM journal_entries WHERE workspace_id = ? AND seq = 3`, workspaceID); err != nil {
		t.Fatalf("compact seq 3: %v", err)
	}
	removed := []journal.RemovedEntry{{Seq: 3, Hash: removedHash}}
	if _, err := source.ExecContext(ctx,
		`INSERT INTO journal_chain_checkpoints (id, workspace_id, removed_json, mac) VALUES (?, ?, ?, ?)`,
		"jck_amp_1", workspaceID,
		fmt.Sprintf(`[{"seq":3,"hash":%q}]`, removedHash),
		journal.CheckpointMAC(key, workspaceID, removed)); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	countSourceCheckpoints := func() int {
		t.Helper()
		var n int
		if err := source.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM journal_chain_checkpoints WHERE workspace_id = ?`,
			workspaceID).Scan(&n); err != nil {
			t.Fatalf("count source checkpoints: %v", err)
		}
		return n
	}
	before := countSourceCheckpoints()
	if before != 1 {
		t.Fatalf("test premise broken: source has %d checkpoints before the fork, want 1", before)
	}

	const passphrase = "checkpoint-amplification-pass-123"
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

	// Fork twice: one duplicate is a bug, and a per-restore duplicate is
	// unbounded growth in the table that records deletions.
	for i, slug := range []string{"e2e-ws-amp-1", "e2e-ws-amp-2"} {
		res, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
			Path:        created.Path,
			Passphrase:  passphrase,
			Actor:       actor,
			AsWorkspace: slug,
		})
		if err != nil {
			t.Fatalf("RestoreBackup --as-workspace=%s: %v", slug, err)
		}
		if after := countSourceCheckpoints(); after != before {
			t.Errorf("after fork %d the SOURCE workspace holds %d checkpoints, want %d: "+
				"the fork's checkpoint row was written into the workspace it forked from",
				i+1, after, before)
		}
		var forked int
		if err := source.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM journal_chain_checkpoints WHERE workspace_id = ?`,
			res.RestoredWorkspaceID).Scan(&forked); err != nil {
			t.Fatalf("count forked checkpoints: %v", err)
		}
		if forked != 1 {
			t.Errorf("fork %d holds %d checkpoints, want 1", i+1, forked)
		}
		if res.JournalCheckpointsResigned != 1 {
			t.Errorf("fork %d reported %d checkpoints re-signed, want 1", i+1, res.JournalCheckpointsResigned)
		}
	}
}

// TestForkedRestore_DoesNotLaunderAForgedCheckpoint is the security guard on
// the re-sign. A compaction checkpoint's MAC is what stops an attacker with DB
// write access from covering a mid-chain delete: they can insert the row, but
// they cannot sign it, so VerifyChain ignores it and the gap still reads as
// tampering.
//
// Re-signing checkpoints under the fork's workspace id is exactly the
// operation that could destroy that property. If the restore re-MACs
// unconditionally, an attacker plants a forged checkpoint on the source, waits
// for anyone to run `--as-workspace`, and gets it back carrying a signature
// this installation vouches for. So a checkpoint is re-signed only if its
// stored MAC already validated where it came from; one that did not is carried
// across untouched, inert in the fork exactly as it was inert in the source.
func TestForkedRestore_DoesNotLaunderAForgedCheckpoint(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 5)

	// Delete seq 3 and cover it with a checkpoint nobody signed — the
	// shape of a malicious mid-chain delete.
	var removedHash string
	if err := source.QueryRowContext(ctx,
		`SELECT entry_hash FROM journal_entries WHERE workspace_id = ? AND seq = 3`,
		workspaceID).Scan(&removedHash); err != nil {
		t.Fatalf("read seq 3 hash: %v", err)
	}
	if _, err := source.ExecContext(ctx,
		`DELETE FROM journal_entries WHERE workspace_id = ? AND seq = 3`, workspaceID); err != nil {
		t.Fatalf("delete seq 3: %v", err)
	}
	if _, err := source.ExecContext(ctx,
		`INSERT INTO journal_chain_checkpoints (id, workspace_id, removed_json, mac) VALUES (?, ?, ?, ?)`,
		"jck_forged_1", workspaceID,
		fmt.Sprintf(`[{"seq":3,"hash":%q}]`, removedHash),
		"deadbeef"); err != nil {
		t.Fatalf("seed forged checkpoint: %v", err)
	}

	// Premise: the forgery does not work on the source.
	res0, err := journal.VerifyChain(ctx, source, workspaceID)
	if err != nil {
		t.Fatalf("VerifyChain(source): %v", err)
	}
	if res0.OK {
		t.Fatalf("test premise broken: an unsigned checkpoint covered a mid-chain delete on the source")
	}

	const passphrase = "forged-checkpoint-pass-123"
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
		AsWorkspace: "e2e-ws-forged",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}

	// And it must not work in the fork either. If the re-sign had blessed
	// the forgery, VerifyChain would bridge the seq-3 gap and report ok.
	got, err := journal.VerifyChain(ctx, source, res.RestoredWorkspaceID)
	if err != nil {
		t.Fatalf("VerifyChain(fork): %v", err)
	}
	if got.OK {
		t.Errorf("the forked restore re-signed a checkpoint whose MAC never validated on the source: "+
			"the fork's chain now reports ok (%d entries, %d checkpoints applied), laundering a mid-chain delete",
			got.Count, got.Checkpoints)
	}
	if got.Checkpoints != 0 {
		t.Errorf("fork applied %d checkpoints, want 0: an unsigned checkpoint must stay unsigned", got.Checkpoints)
	}
	if res.JournalCheckpointsResigned != 0 {
		t.Errorf("restore reported %d checkpoints re-signed, want 0", res.JournalCheckpointsResigned)
	}
}
