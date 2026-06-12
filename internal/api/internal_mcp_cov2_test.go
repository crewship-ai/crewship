package api

// Second coverage pass for internal_mcp.go — resolveOneEnvVar's non-NoRows
// scan-error warn branch. (ensureFreshOAuthToken's post-refresh persistence
// tail needs a successful token refresh, which the SSRF-guarded transport
// makes unreachable offline — covered branches stop at the refresh call.)

import (
	"context"
	"testing"
)

func TestMCP2_ResolveOneEnvVar_ScanError(t *testing.T) {
	db, wsID, _ := covMCPRig(t)
	if _, err := db.Exec(`ALTER TABLE credentials RENAME TO credentials_hidden_mcp2`); err != nil {
		t.Fatalf("rename credentials: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`ALTER TABLE credentials_hidden_mcp2 RENAME TO credentials`) })

	entry, ok := resolveOneEnvVar(context.Background(), db, newTestLogger(), wsID, "SLACK_BOT_TOKEN")
	if ok {
		t.Errorf("expected no match on DB error, got %+v", entry)
	}
}
