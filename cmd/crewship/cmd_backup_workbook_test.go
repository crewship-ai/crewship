package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// --discard-workbooks is the CLI equivalent of the API's discard_workbooks:
// it acknowledges that a replace restore destroys the target's workbooks.
func TestBackupRestoreRunE_DiscardWorkbooks(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	covSetFlagCli5(t, backupRestoreCmd, "discard-workbooks", "true")
	var err error
	covCaptureAll(t, func() { err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/backups/ws.tar"}) })
	if err == nil || !strings.Contains(err.Error(), "--discard-workbooks") {
		t.Fatalf("--discard-workbooks without --replace = %v, want refusal", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/admin/backups/restore"); len(calls) != 0 {
		t.Fatalf("refused flag combination reached the server: %d calls", len(calls))
	}

	covSetFlagCli5(t, backupRestoreCmd, "replace", "true")
	stub.OnPost("/api/v1/admin/backups/restore", clitest.JSONResponse(200, map[string]any{
		"restored_ws": "acme", "restored_workspace_id": covWSCli5, "crews_count": 0, "rows_inserted": 1,
	}))
	covCaptureAll(t, func() { err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/backups/ws.tar"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups/restore")[0].Body, &body)
	if body["replace"] != true || body["discard_workbooks"] != true {
		t.Fatalf("restore body = %v, want replace and discard_workbooks", body)
	}
}
