package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestWorkspaceRestorePreservesWholeRunApprovalOptIn(t *testing.T) {
	source := testutil.MigratedSQLDB(t)
	for _, statement := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('gate-backup-ws','Gate backup','gate-backup-ws')`,
		`INSERT INTO crews(id,workspace_id,name,slug,whole_run_approval) VALUES('gate-backup-crew','gate-backup-ws','Gate crew','gate-backup-crew',1)`,
	} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, "gate-backup-ws")
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.MigratedSQLDB(t)
	if err := backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	evaluator, err := harbormaster.EvaluatorForCrew(t.Context(), target, "gate-backup-ws", "gate-backup-crew", nil)
	if err != nil {
		t.Fatal(err)
	}
	if required, _, _ := evaluator.Evaluate(t.Context(), "agent_run", map[string]any{"agent_slug": "synthetic", "agent_role": "WORKER"}); !required {
		t.Fatal("restore silently disabled whole-run approval")
	}
}
