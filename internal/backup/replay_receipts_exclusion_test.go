package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestReplayReceiptsRemainInstallationLocalAcrossWorkspaceRestore(t *testing.T) {
	ctx := t.Context()
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	if _, err := source.ExecContext(ctx, `INSERT INTO journal_replay_receipts(id,fingerprint,workspace_id,agent_id,trace_id,entry_type) VALUES('retained:source:1:0','source-only',?,'source-agent','source-run','exec.output_chunk')`, workspace); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := dump.Tables["journal_replay_receipts"]; present {
		t.Fatal("workspace bundle exports source-installation replay authority")
	}
	// A supplied bundle must not inject authority for a source spool either.
	dump.Tables["journal_replay_receipts"] = []map[string]any{{
		"id": "retained:source:1:0", "fingerprint": "source-only",
		"workspace_id": workspace, "agent_id": "source-agent",
		"trace_id": "source-run", "entry_type": "exec.output_chunk",
	}}
	target := openMigratedDB(t)
	if err := backup.RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_replay_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("restore transplanted replay authority: count=%d err=%v", count, err)
	}
}
