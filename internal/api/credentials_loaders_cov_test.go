package api

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// credentials_loaders_cov_test.go covers the CredentialHandler batch
// loaders: success loops, query-error fallbacks and setCrewIDs error
// arms. Helpers prefixed covCL.

func covCLSeed(t *testing.T) (*CredentialHandler, *sql.DB, string, string) {
	t.Helper()
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covcl-crew', ?, 'C', 'covcl-c')`, wsID)
	seedAgentRow(t, db, "covcl-ag", wsID, "covcl-crew", "Agent CL", "covcl-a", "AGENT")
	seedCredentialEnc(t, db, wsID, userID, "covcl-cred", "covcl-name", "secret")
	h := NewCredentialHandler(db, newTestLogger())
	return h, db, wsID, userID
}

func TestCovCL_LoadAgentNamesBatch(t *testing.T) {
	h, db, _, _ := covCLSeed(t)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('covcl-ac', 'covcl-ag', 'covcl-cred', 'X', 0, datetime('now'))`)

	got := h.loadAgentNamesBatch(context.Background(), []string{"covcl-cred", "covcl-other"})
	if !reflect.DeepEqual(got["covcl-cred"], []string{"Agent CL"}) {
		t.Errorf("names = %v, want [Agent CL]", got["covcl-cred"])
	}
	if _, ok := got["covcl-other"]; ok {
		t.Errorf("unexpected entry for unbound credential")
	}

	// Query error -> empty map, no panic.
	db.Close()
	got = h.loadAgentNamesBatch(context.Background(), []string{"covcl-cred"})
	if len(got) != 0 {
		t.Errorf("after close: %v, want empty", got)
	}
}

func TestCovCL_LoadMCPUsedBatch(t *testing.T) {
	h, db, wsID, _ := covCLSeed(t)
	execOrFatal(t, db, `INSERT INTO workspace_mcp_servers
		(id, workspace_id, name, display_name, transport, endpoint, enabled)
		VALUES ('covcl-srv', ?, 'covcl-srv', 'CL Server', 'http', 'http://example.invalid', 1)`, wsID)
	execOrFatal(t, db, `INSERT INTO agent_mcp_bindings
		(id, agent_id, mcp_server_id, mcp_server_scope, credential_id, enabled)
		VALUES ('covcl-mcp', 'covcl-ag', 'covcl-srv', 'workspace', 'covcl-cred', 1)`)

	got := h.loadMCPUsedBatch(context.Background(), []string{"covcl-cred", "covcl-none"})
	if !got["covcl-cred"] {
		t.Errorf("covcl-cred not marked as MCP-used: %v", got)
	}
	if got["covcl-none"] {
		t.Errorf("covcl-none must not be marked")
	}

	db.Close()
	got = h.loadMCPUsedBatch(context.Background(), []string{"covcl-cred"})
	if len(got) != 0 {
		t.Errorf("after close: %v, want empty", got)
	}
}

func TestCovCL_LoadCrewIDs(t *testing.T) {
	h, db, _, _ := covCLSeed(t)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('covcl-cred', 'covcl-crew')`)

	got := h.loadCrewIDs(context.Background(), "covcl-cred")
	if !reflect.DeepEqual(got, []string{"covcl-crew"}) {
		t.Errorf("crew ids = %v, want [covcl-crew]", got)
	}
	// Unbound credential -> empty non-nil slice.
	if got := h.loadCrewIDs(context.Background(), "covcl-unbound"); got == nil || len(got) != 0 {
		t.Errorf("unbound = %v, want empty slice", got)
	}

	db.Close()
	if got := h.loadCrewIDs(context.Background(), "covcl-cred"); len(got) != 0 {
		t.Errorf("after close: %v, want empty", got)
	}
}

func TestCovCL_LoadCrewIDsBatch(t *testing.T) {
	h, db, _, _ := covCLSeed(t)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('covcl-cred', 'covcl-crew')`)

	got := h.loadCrewIDsBatch(context.Background(), []string{"covcl-cred"})
	if !reflect.DeepEqual(got["covcl-cred"], []string{"covcl-crew"}) {
		t.Errorf("batch = %v", got)
	}

	db.Close()
	if got := h.loadCrewIDsBatch(context.Background(), []string{"covcl-cred"}); len(got) != 0 {
		t.Errorf("after close: %v, want empty", got)
	}
}

func TestCovCL_SetCrewIDs(t *testing.T) {
	h, db, _, _ := covCLSeed(t)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('covcl-cred', 'covcl-crew')`)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covcl-crew2', (SELECT workspace_id FROM crews WHERE id='covcl-crew'), 'C2', 'covcl-c2')`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.setCrewIDs(context.Background(), tx, "covcl-cred", []string{"covcl-crew2"}); err != nil {
		t.Fatalf("setCrewIDs: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	got := h.loadCrewIDs(context.Background(), "covcl-cred")
	if !reflect.DeepEqual(got, []string{"covcl-crew2"}) {
		t.Errorf("after replace = %v, want [covcl-crew2]", got)
	}
}

func TestCovCL_SetCrewIDs_InsertError(t *testing.T) {
	h, db, _, _ := covCLSeed(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	// FK violation: crew does not exist.
	err = h.setCrewIDs(context.Background(), tx, "covcl-cred", []string{"covcl-ghost-crew"})
	if err == nil || !strings.Contains(err.Error(), "insert crew") {
		t.Fatalf("err = %v, want insert crew error", err)
	}
}

func TestCovCL_SetCrewIDs_DeleteError(t *testing.T) {
	h, db, _, _ := covCLSeed(t)
	execOrFatal(t, db, `CREATE TRIGGER covcl_fail_del BEFORE DELETE ON credential_crews BEGIN SELECT RAISE(ABORT, 'covcl boom'); END`)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('covcl-cred', 'covcl-crew')`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	err = h.setCrewIDs(context.Background(), tx, "covcl-cred", nil)
	if err == nil || !strings.Contains(err.Error(), "delete existing crew bindings") {
		t.Fatalf("err = %v, want delete error", err)
	}
}
