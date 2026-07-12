package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestLogsAgentMissIsExitNotFound: cmd_logs resolves its agent with an
// INLINE lookup (not resolveAgentID), which the #958 typed-exit sweep
// missed — caught live on dev2 (`crewship logs <typo>` exited 1, the
// documented contract says 3). Locks the straggler sweep.
func TestLogsAgentMissIsExitNotFound(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": "cabcdefghijklmnopqrst", "slug": "viktor", "name": "Viktor"},
	}))

	err := logsCmd.RunE(logsCmd, []string{"neexistujici-agent"})
	if err == nil {
		t.Fatal("expected a not-found error")
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitNotFound {
		t.Errorf("ExitCodeFor(%v) = %d, want %d (ExitNotFound)", err, code, cli.ExitNotFound)
	}
}
