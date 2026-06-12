package api

// crew_integrations.go coverage top-up #2 — the auto-migrate warn fork,
// the lax-schema scan-skip branches, the auth-status batch query error,
// and the full auth-status mapping matrix (none / missing / expired /
// connected) for both ListAllCrewIntegrations and ListCrewIntegrations.
//
// All tests are prefixed TestCov2CI.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cov2CIRig(t *testing.T) (*IntegrationHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "ci2-crew", wsID, "Crew", "ci2-crew")
	return NewIntegrationHandler(db, newTestLogger()), db, wsID, crewID
}

func cov2CISeedServer(t *testing.T, db *sql.DB, id, crewID, name, transport, endpoint string) {
	t.Helper()
	var ep any
	if endpoint != "" {
		ep = endpoint
	}
	if _, err := db.Exec(`INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport, endpoint)
		VALUES (?, ?, ?, ?, ?, ?)`, id, crewID, name, name, transport, ep); err != nil {
		t.Fatalf("seed server %s: %v", id, err)
	}
}

func cov2CIBindCredential(t *testing.T, db *sql.DB, bindID, agentID, serverID, credID, wsID, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'enc', 'API_KEY', 'NONE', 'WORKSPACE', ?, 'test-user-id', datetime('now'), datetime('now'))`,
		credID, wsID, "cred-"+credID, status); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, credential_id)
		VALUES (?, ?, ?, 'crew', ?)`, bindID, agentID, serverID, credID); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
}

func cov2CIListAllReq(wsID string) *http.Request {
	req := httptest.NewRequest("GET", "/x", nil)
	return req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
}

func cov2CIListCrewReq(wsID, crewID string) *http.Request {
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("crewId", crewID)
	return req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
}

// --- auth status mapping matrix across both list endpoints ---

func TestCov2CIAuthStatusMapping(t *testing.T) {
	h, db, wsID, crewID := cov2CIRig(t)
	agentID := seedAgentRow(t, db, "ci2-agent", wsID, crewID, "A", "ci2-agent", "AGENT")

	cov2CISeedServer(t, db, "srv-stdio", crewID, "stdio-one", "stdio", "")
	cov2CISeedServer(t, db, "srv-nocred", crewID, "http-nocred", "streamable-http", "https://mcp.example/a")
	cov2CISeedServer(t, db, "srv-expired", crewID, "http-expired", "streamable-http", "https://mcp.example/b")
	cov2CISeedServer(t, db, "srv-active", crewID, "http-active", "streamable-http", "https://mcp.example/c")
	cov2CIBindCredential(t, db, "b-exp", agentID, "srv-expired", "cred-exp", wsID, "EXPIRED")
	cov2CIBindCredential(t, db, "b-act", agentID, "srv-active", "cred-act", wsID, "ACTIVE")

	want := map[string]string{
		"srv-stdio":   "none",
		"srv-nocred":  "missing",
		"srv-expired": "expired",
		"srv-active":  "connected",
	}

	check := func(t *testing.T, body []byte) {
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
		if len(rows) != 4 {
			t.Fatalf("rows = %d, want 4", len(rows))
		}
		for _, row := range rows {
			id, _ := row["id"].(string)
			got, _ := row["auth_status"].(string)
			if got != want[id] {
				t.Errorf("server %s auth_status = %q, want %q", id, got, want[id])
			}
		}
	}

	t.Run("ListAllCrewIntegrations", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListAllCrewIntegrations(rec, cov2CIListAllReq(wsID))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		check(t, rec.Body.Bytes())
	})

	t.Run("ListCrewIntegrations", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ListCrewIntegrations(rec, cov2CIListCrewReq(wsID, crewID))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		check(t, rec.Body.Bytes())
	})
}

// --- auto-migrate warn: malformed JSON blob doesn't break the list ---

func TestCov2CIAutoMigrate_BadBlobIsWarnOnly(t *testing.T) {
	h, db, wsID, crewID := cov2CIRig(t)
	if _, err := db.Exec(`UPDATE crews SET mcp_config_json = '{not json' WHERE id = ?`, crewID); err != nil {
		t.Fatalf("set blob: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, cov2CIListAllReq(wsID))
	if rec.Code != http.StatusOK {
		t.Fatalf("ListAll status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ListCrewIntegrations(rec, cov2CIListCrewReq(wsID, crewID))
	if rec.Code != http.StatusOK {
		t.Fatalf("ListCrew status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// --- scan-skip: a NULL display_name row is skipped, not fatal ---

func TestCov2CIScanError_RowSkipped(t *testing.T) {
	h, db, wsID, crewID := cov2CIRig(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE crew_mcp_servers`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE crew_mcp_servers (
		id TEXT PRIMARY KEY, crew_id TEXT, workspace_mcp_server_id TEXT,
		name TEXT, display_name TEXT, transport TEXT DEFAULT 'streamable-http',
		endpoint TEXT, command TEXT, args_json TEXT, env_json TEXT, config_json TEXT,
		icon TEXT, enabled INTEGER DEFAULT 1, deleted_at TEXT,
		created_at TEXT DEFAULT (datetime('now')), updated_at TEXT DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	// Good row + NULL-name row (name scans into a plain string).
	cov2CISeedServer(t, db, "srv-good", crewID, "good", "stdio", "")
	if _, err := db.Exec(`INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport)
		VALUES ('srv-bad', ?, NULL, 'Bad', 'stdio')`, crewID); err != nil {
		t.Fatalf("insert bad: %v", err)
	}

	for _, tc := range []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"ListAll", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			h.ListAllCrewIntegrations(rec, cov2CIListAllReq(wsID))
			return rec
		}},
		{"ListCrew", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			h.ListCrewIntegrations(rec, cov2CIListCrewReq(wsID, crewID))
			return rec
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.call()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "srv-good") {
				t.Errorf("body = %s, want good row", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "srv-bad") {
				t.Errorf("body = %s, NULL-name row must be skipped", rec.Body.String())
			}
		})
	}
}

// --- auth-status batch query failure → 500 ---

func TestCov2CIAuthStatusQueryError500(t *testing.T) {
	h, db, wsID, crewID := cov2CIRig(t)
	cov2CISeedServer(t, db, "srv-x", crewID, "x", "streamable-http", "https://mcp.example/x")
	// credentials is only touched by the auth-status JOIN.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE credentials`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ListAllCrewIntegrations(rec, cov2CIListAllReq(wsID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("ListAll status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ListCrewIntegrations(rec, cov2CIListCrewReq(wsID, crewID))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("ListCrew status = %d, want 500 (populateAuthStatus), body=%s", rec.Code, rec.Body.String())
	}
}
