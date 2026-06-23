package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// credentials_loaders_cov2_test.go — the query-failure fallbacks of the
// batch loaders (closed DB → empty results, never a panic) and the
// setCrewIDs error returns (blocked DELETE / INSERT inside the tx).
// Helpers prefixed covCL2.

func TestCovCL2_BatchLoaders_QueryFailure_ReturnEmpty(t *testing.T) {
	db := setupTestDB(t)
	h := NewCredentialHandler(db, newTestLogger())
	db.Close()
	ctx := context.Background()

	if got := h.loadAgentNamesBatch(ctx, []string{"c1"}); len(got) != 0 {
		t.Errorf("loadAgentNamesBatch on closed db = %v, want empty", got)
	}
	if got := h.loadMCPUsedBatch(ctx, []string{"c1"}); len(got) != 0 {
		t.Errorf("loadMCPUsedBatch on closed db = %v, want empty", got)
	}
	if got := h.loadCrewIDs(ctx, "c1"); len(got) != 0 {
		t.Errorf("loadCrewIDs on closed db = %v, want empty slice", got)
	}
	if got := h.loadCrewIDsBatch(ctx, []string{"c1"}); len(got) != 0 {
		t.Errorf("loadCrewIDsBatch on closed db = %v, want empty", got)
	}
}

func TestCovCL2_SetCrewIDs_DeleteBlocked_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covcl2-crew", wsID, "C", "covcl2-crew")
	seedCredentialRowForCovCL2(t, db, "covcl2-cred", wsID, userID)
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES ('covcl2-cred', ?)`, crewID)
	execOrFatal(t, db, `CREATE TRIGGER covcl2_block_del BEFORE DELETE ON credential_crews
		BEGIN SELECT RAISE(ABORT, 'covcl2 forced'); END`)

	h := NewCredentialHandler(db, newTestLogger())
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	err = h.setCrewIDs(context.Background(), tx, "covcl2-cred", []string{crewID})
	if err == nil || !strings.Contains(err.Error(), "delete existing crew bindings") {
		t.Fatalf("err = %v, want delete-bindings failure", err)
	}
}

func TestCovCL2_SetCrewIDs_InsertBlocked_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covcl2-crew2", wsID, "C", "covcl2-crew2")
	seedCredentialRowForCovCL2(t, db, "covcl2-cred2", wsID, userID)
	execOrFatal(t, db, `CREATE TRIGGER covcl2_block_ins BEFORE INSERT ON credential_crews
		BEGIN SELECT RAISE(ABORT, 'covcl2 forced'); END`)

	h := NewCredentialHandler(db, newTestLogger())
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	err = h.setCrewIDs(context.Background(), tx, "covcl2-cred2", []string{crewID})
	if err == nil || !strings.Contains(err.Error(), "insert crew") {
		t.Fatalf("err = %v, want insert-crew failure", err)
	}
}

// seedCredentialRowForCovCL2 inserts a minimal credential row.
func seedCredentialRowForCovCL2(t *testing.T, db *sql.DB, id, wsID, userID string) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'enc', 'API_KEY', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		id, wsID, "cred-"+id, userID)
}
