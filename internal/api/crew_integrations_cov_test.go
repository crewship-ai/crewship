package api

// Coverage tests for crew_integrations.go — ListAllCrewIntegrations and
// ListCrewIntegrations auth-status mapping, blob auto-migration, empty
// results, and DB error paths. Reuses the covInt* seed helpers from
// integrations_cov_test.go.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func covCIRig(t *testing.T) (*IntegrationHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewIntegrationHandler(db, newTestLogger()), db, userID, wsID
}

func covCISeedCrew(t *testing.T, db *sql.DB, id, wsID, slug string, mcpBlob any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, mcp_config_json) VALUES (?, ?, ?, ?, ?)`,
		id, wsID, "Crew "+slug, slug, mcpBlob); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
}

func covCISeedAgent(t *testing.T, db *sql.DB, id, wsID, crewID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES (?, ?, ?, 'A', ?)`,
		id, wsID, crewID, "ag-"+id); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

// TestCovCIListAll_AuthStatusMapping seeds three crew servers with different
// credential states and checks the computed auth_status for each, plus the
// crew_name/crew_slug join columns.
func TestCovCIListAll_AuthStatusMapping(t *testing.T) {
	h, db, userID, wsID := covCIRig(t)
	covCISeedCrew(t, db, "crew-ci", wsID, "ci", nil)
	covCISeedAgent(t, db, "ag-ci", wsID, "crew-ci")

	// connected: streamable-http + ACTIVE credential binding
	covIntCrewServer(t, db, "srv-conn", "crew-ci", "github", "streamable-http", "https://mcp.example/gh")
	covIntCredential(t, db, "cred-active", wsID, userID, "gh-token", "ACTIVE")
	covIntBinding(t, db, "b-conn", "ag-ci", "srv-conn", "crew", "cred-active")

	// expired: streamable-http + EXPIRED credential binding
	covIntCrewServer(t, db, "srv-exp", "crew-ci", "jira", "streamable-http", "https://mcp.example/jira")
	covIntCredential(t, db, "cred-exp", wsID, userID, "jira-token", "EXPIRED")
	covIntBinding(t, db, "b-exp", "ag-ci", "srv-exp", "crew", "cred-exp")

	// missing: streamable-http, no credential binding
	covIntCrewServer(t, db, "srv-miss", "crew-ci", "linear", "streamable-http", "https://mcp.example/linear")

	// none: stdio transport
	covIntCrewServer(t, db, "srv-stdio", "crew-ci", "local", "stdio", "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/crews", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var out []crewIntegrationOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	got := map[string]string{}
	for _, s := range out {
		got[s.ID] = s.AuthStatus
		if s.CrewSlug != "ci" {
			t.Errorf("crew_slug = %q for %s", s.CrewSlug, s.ID)
		}
	}
	want := map[string]string{
		"srv-conn":  "connected",
		"srv-exp":   "expired",
		"srv-miss":  "missing",
		"srv-stdio": "none",
	}
	for id, status := range want {
		if got[id] != status {
			t.Errorf("auth_status[%s] = %q, want %q", id, got[id], status)
		}
	}
}

// TestCovCIListAll_BlobAutoMigration plants a legacy mcp_config_json blob on
// the crew; listing must auto-migrate it into crew_mcp_servers and clear the
// blob.
func TestCovCIListAll_BlobAutoMigration(t *testing.T) {
	h, db, userID, wsID := covCIRig(t)
	blob := `{"mcpServers":{"legacy-http":{"url":"https://mcp.example/legacy","type":"http"}}}`
	covCISeedCrew(t, db, "crew-blob", wsID, "blob", blob)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/crews", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var out []crewIntegrationOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Name != "legacy-http" {
		t.Fatalf("migrated server not listed: %+v", out)
	}
	var blobAfter sql.NullString
	if err := db.QueryRow(`SELECT mcp_config_json FROM crews WHERE id = 'crew-blob'`).Scan(&blobAfter); err != nil {
		t.Fatalf("query: %v", err)
	}
	if blobAfter.Valid && blobAfter.String != "" {
		t.Errorf("mcp_config_json not cleared after migration: %q", blobAfter.String)
	}
}

func TestCovCIListAll_Empty(t *testing.T) {
	h, _, userID, wsID := covCIRig(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/crews", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "[]\n" && rec.Body.String() != "[]" {
		t.Errorf("empty list should serialize as []; got %q", rec.Body.String())
	}
}

func TestCovCIListAll_DBError500(t *testing.T) {
	h, db, userID, wsID := covCIRig(t)
	db.Close()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/crews", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- ListCrewIntegrations -------------------------------------------------

func covCIListCrewReq(userID, wsID, crewID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/"+crewID+"/integrations", nil)
	req.SetPathValue("crewId", crewID)
	return withWorkspaceUser(req, userID, wsID, "OWNER")
}

func TestCovCIListCrew_AuthStatusAndBlobMigration(t *testing.T) {
	h, db, userID, wsID := covCIRig(t)
	blob := `{"mcpServers":{"from-blob":{"command":"npx","args":["-y","x"]}}}`
	covCISeedCrew(t, db, "crew-l", wsID, "l", blob)
	covCISeedAgent(t, db, "ag-l", wsID, "crew-l")

	covIntCrewServer(t, db, "srv-l1", "crew-l", "github", "streamable-http", "https://mcp.example/gh")
	covIntCredential(t, db, "cred-l1", wsID, userID, "tok", "ACTIVE")
	covIntBinding(t, db, "b-l1", "ag-l", "srv-l1", "crew", "cred-l1")

	rec := httptest.NewRecorder()
	h.ListCrewIntegrations(rec, covCIListCrewReq(userID, wsID, "crew-l"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var out []crewMCPServerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Blob server migrated + the seeded one.
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (seeded + migrated blob): %+v", len(out), out)
	}
	statuses := map[string]string{}
	names := map[string]bool{}
	for _, s := range out {
		statuses[s.ID] = s.AuthStatus
		names[s.Name] = true
	}
	if statuses["srv-l1"] != "connected" {
		t.Errorf("srv-l1 auth_status = %q, want connected", statuses["srv-l1"])
	}
	if !names["from-blob"] {
		t.Error("blob-migrated server missing from list")
	}
}

func TestCovCIListCrew_EmptyAndNotFound(t *testing.T) {
	h, db, userID, wsID := covCIRig(t)
	covCISeedCrew(t, db, "crew-empty", wsID, "empty", nil)

	t.Run("empty list", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListCrewIntegrations(rec, covCIListCrewReq(userID, wsID, "crew-empty"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var out []crewMCPServerResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("len = %d, want 0", len(out))
		}
	})

	t.Run("crew not found 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListCrewIntegrations(rec, covCIListCrewReq(userID, wsID, "nope"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestCovCIListCrew_DBError500(t *testing.T) {
	h, db, userID, wsID := covCIRig(t)
	db.Close()
	rec := httptest.NewRecorder()
	h.ListCrewIntegrations(rec, covCIListCrewReq(userID, wsID, "crew-x"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
