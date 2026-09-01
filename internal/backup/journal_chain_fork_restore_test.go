package backup_test

// Does a forked restore (`--as-workspace`) leave the journal hash-chain
// verifiable?
//
// The chain's entry_hash is an HMAC over, among other columns, the entry's own
// id, its workspace_id, crew_id, agent_id and mission_id (see
// journal.ChainHashKeyed / journal.ChainFields). RemapIDs regenerates exactly
// those values — the PK of every journal_entries row plus every FK column
// SQLite reports for the table — but the stored prev_hash / entry_hash ride
// through untouched.
//
// These tests assert what SHOULD hold: a restore hands back a database whose
// journal still verifies. The plain (non-forked) restore is included as the
// control, so a failure can be attributed to the remap rather than to the
// round-trip.

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

// seedJournalChain appends n chained journal entries to workspaceID, computing
// prev_hash/entry_hash exactly the way the emit path and the v152 migration
// backfill do, so the resulting rows are indistinguishable from ones written by
// the Writer. Entries alternate crew/agent so the remap has FK columns to
// rewrite as well as the PK.
func seedJournalChain(t *testing.T, db *sql.DB, workspaceID string, n int) {
	t.Helper()
	ctx := context.Background()

	// seedWorkspace plants two legacy, UNCHAINED rows (seq 0, no hashes).
	// VerifyChain reports those as a sequence disorder regardless of
	// backup/restore, which would mask the property under test. Drop them so
	// the workspace holds exactly one well-formed chain.
	if _, err := db.ExecContext(ctx,
		`DELETE FROM journal_entries WHERE workspace_id = ? AND seq = 0`, workspaceID); err != nil {
		t.Fatalf("clear unchained seed entries: %v", err)
	}

	key := journal.ChainKeyFromEnv()
	prev := journal.GenesisPrevHash

	crews := []string{"c_alpha", "c_beta"}
	agents := []string{"a_alice", "a_carol"}

	for i := 1; i <= n; i++ {
		f := journal.ChainFields{
			Seq:       int64(i),
			ID:        fmt.Sprintf("je_chain_%02d", i),
			Workspace: workspaceID,
			CrewID:    crews[i%len(crews)],
			AgentID:   agents[i%len(agents)],
			TS:        fmt.Sprintf("2026-01-01T00:00:%02d.000Z", i),
			EntryType: "mission.status_change",
			Severity:  "info",
			Priority:  "normal",
			ActorType: "system",
			ActorID:   "u_admin",
			Summary:   fmt.Sprintf("chained entry %d", i),
			Payload:   `{}`,
			Refs:      `{}`,
		}
		hash := journal.ChainHashKeyed(key, prev, f)

		if _, err := db.ExecContext(ctx, `INSERT INTO journal_entries
			(id, workspace_id, crew_id, agent_id, ts, entry_type, severity, priority,
			 actor_type, actor_id, summary, payload, refs,
			 seq, prev_hash, entry_hash, priority_at_emit)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.Workspace, f.CrewID, f.AgentID, f.TS, f.EntryType, f.Severity, f.Priority,
			f.ActorType, f.ActorID, f.Summary, f.Payload, f.Refs,
			f.Seq, prev, hash, f.Priority); err != nil {
			t.Fatalf("seed journal entry %d: %v", i, err)
		}
		prev = hash
	}
}

// mustVerify asserts the chain for workspaceID verifies clean.
func mustVerify(t *testing.T, db *sql.DB, workspaceID, what string) {
	t.Helper()
	res, err := journal.VerifyChain(context.Background(), db, workspaceID)
	if err != nil {
		t.Fatalf("%s: VerifyChain returned an error: %v", what, err)
	}
	if !res.OK {
		t.Errorf("%s: journal chain does not verify for workspace %s\n"+
			"  reason:      %s\n"+
			"  broken seq:  %d\n"+
			"  broken id:   %s\n"+
			"  entries:     %d\n"+
			"  break count: %d of %d entries",
			what, workspaceID, res.Reason, res.BrokenSeq, res.BrokenID, res.Count,
			res.BreakCount, res.Count)
	}
	if res.Count == 0 {
		t.Errorf("%s: VerifyChain walked 0 entries for workspace %s — nothing was actually checked",
			what, workspaceID)
	}
}

// TestJournalChain_SurvivesForkedRestore is the reproduction: back a workspace
// up, restore it under a NEW workspace identity with --as-workspace, and verify
// the restored chain.
func TestJournalChain_SurvivesForkedRestore(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 5)

	// Precondition: the source chain is intact. If this fails the seed is
	// wrong and nothing below means anything.
	mustVerify(t, source, workspaceID, "source (pre-backup)")

	const passphrase = "forked-restore-chain-pass-123"
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

	// Fork it beside the original, on the SAME instance — the documented
	// --as-workspace use case.
	res, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:        created.Path,
		Passphrase:  passphrase,
		Actor:       actor,
		AsWorkspace: "e2e-ws-fork",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	if res.RestoredWorkspaceID == "" {
		t.Fatalf("restore reported no RestoredWorkspaceID; nothing to verify")
	}
	if res.RestoredWorkspaceID == workspaceID {
		t.Fatalf("--as-workspace did not remap the workspace id (still %s)", workspaceID)
	}

	// The five seeded rows, plus ONE entry recording that the chain was
	// re-signed at restore.
	//
	// This assertion was written as `want 5` when the reproduction was
	// filed, before the fix's shape was chosen. It is tightened here, not
	// relaxed: the re-sign entry is the whole reason option 1 (re-chain as
	// a new genesis) was picked over refusing --as-workspace outright. A
	// fork's chain no longer links back to the source, and a chain that
	// verified clean while saying nothing about that would assert stronger
	// provenance than the data supports. So the count is 5 restored + 1
	// notice, and both halves are checked separately — a bare `want 6`
	// would pass if the notice landed and a restored row did not.
	var got int
	if err := source.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND entry_type != ?`,
		res.RestoredWorkspaceID, string(journal.EntryBackupChainResigned)).Scan(&got); err != nil {
		t.Fatalf("count restored entries: %v", err)
	}
	if got != 5 {
		t.Fatalf("forked workspace has %d restored journal entries, want 5", got)
	}
	var resigned int
	if err := source.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND entry_type = ?`,
		res.RestoredWorkspaceID, string(journal.EntryBackupChainResigned)).Scan(&resigned); err != nil {
		t.Fatalf("count re-sign entries: %v", err)
	}
	if resigned != 1 {
		t.Fatalf("forked workspace has %d %s entries, want exactly 1: a re-signed chain must say so",
			resigned, journal.EntryBackupChainResigned)
	}

	// The original must still verify — the fork must not disturb it.
	mustVerify(t, source, workspaceID, "source (post-fork)")

	// And the fork itself must verify: restore reported success, so the
	// data it handed back must not read as tampered.
	mustVerify(t, source, res.RestoredWorkspaceID, "forked workspace (--as-workspace)")
}

// TestJournalChain_SurvivesPlainRestore is the control: the same round-trip
// with no --as-workspace, into a clean target. Nothing is remapped here, so
// this isolates whether any chain breakage is remap-specific.
func TestJournalChain_SurvivesPlainRestore(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 5)
	mustVerify(t, source, workspaceID, "source (pre-backup)")

	const passphrase = "plain-restore-chain-pass-123"
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
	if _, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       created.Path,
		Passphrase: passphrase,
		Actor:      actor,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	mustVerify(t, target, workspaceID, "target (plain restore)")
}

// TestJournalChainCheckpoints_OrphanedByForkedRestore isolates the second half
// of the same problem. journal_chain_checkpoints.workspace_id carries NO
// SQLite foreign key (see migrationJournalHashChain), so introspectForeignKeys
// reports no edge for it and RemapIDs pass 2 never rewrites it — while pass 1
// does regenerate the checkpoint's own PK. A checkpoint therefore lands under
// the ORIGINAL workspace id, invisible to the fork, and its MAC commits to that
// original id anyway (journal.CheckpointMAC frames workspaceID), so it could
// not be re-pointed by rewriting the column alone.
//
// Consequence: a forked restore of a journal that has been legitimately
// compacted has an uncovered seq gap, which VerifyChain reads as a malicious
// mid-chain delete.
func TestJournalChainCheckpoints_OrphanedByForkedRestore(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedJournalChain(t, source, workspaceID, 5)

	// Legitimately compact seq 3 out of the chain, recording the signed
	// checkpoint that keeps the resulting gap from reading as tampering.
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
		"jck_fork_1", workspaceID,
		fmt.Sprintf(`[{"seq":3,"hash":%q}]`, removedHash),
		journal.CheckpointMAC(key, workspaceID, removed)); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	// Precondition: the compacted source still verifies — the checkpoint
	// bridges the gap.
	mustVerify(t, source, workspaceID, "source (compacted, checkpointed)")

	const passphrase = "checkpoint-fork-pass-123"
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
		AsWorkspace: "e2e-ws-fork-ckpt",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}

	var ckpts int
	if err := source.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_chain_checkpoints WHERE workspace_id = ?`,
		res.RestoredWorkspaceID).Scan(&ckpts); err != nil {
		t.Fatalf("count forked checkpoints: %v", err)
	}
	if ckpts == 0 {
		t.Errorf("forked workspace %s has no compaction checkpoint: the bundle carried one, "+
			"but RemapIDs left journal_chain_checkpoints.workspace_id pointing at the source workspace %s",
			res.RestoredWorkspaceID, workspaceID)
	}

	// And the fork's chain must verify.
	mustVerify(t, source, res.RestoredWorkspaceID, "forked workspace (compacted, --as-workspace)")
}
