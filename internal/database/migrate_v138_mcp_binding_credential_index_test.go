package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV138_MCPBindingCredentialIndex verifies the #1042 perf migration:
// the index on agent_mcp_bindings(credential_id) exists after Migrate, so the
// credential-list (loadMCPUsedBatch) and delete paths that filter by
// credential_id stop full-scanning the table.
func TestMigrateV138_MCPBindingCredentialIndex(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v138.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var name string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name=?`,
		"idx_agent_mcp_bindings_credential_id").Scan(&name); err != nil {
		t.Fatalf("index idx_agent_mcp_bindings_credential_id missing: %v", err)
	}

	// The query planner should use the index for a credential_id lookup rather
	// than scanning the table.
	var plan string
	if err := db.QueryRow(
		`EXPLAIN QUERY PLAN SELECT credential_id FROM agent_mcp_bindings WHERE credential_id = 'x'`).
		Scan(new(int), new(int), new(int), &plan); err != nil {
		t.Fatalf("explain: %v", err)
	}
	// SQLite reports "SEARCH … USING INDEX idx_agent_mcp_bindings_credential_id"
	// when the index is picked; a full scan says "SCAN".
	if !strings.Contains(plan, "idx_agent_mcp_bindings_credential_id") {
		t.Errorf("query plan did not use the credential_id index: %q", plan)
	}
}
