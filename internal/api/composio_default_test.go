package api

// Tests for ensureDefaultComposioServer — the default-connector provisioning
// helper: single-user auto-derive persists the default; zero/multiple users
// surface operator-actionable errors.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func TestEnsureDefault_SingleUser_AutoDerive(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t,
		`{"items":[{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`,
		`{"items":[{"id":"ca_1","user_id":"user-a","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_gm","auth_scheme":"OAUTH2","is_composio_managed":true}}]}`)

	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL, DefaultConnector: true,
	})

	req := withWorkspaceUser(httptest.NewRequest("PUT", "/", nil), userID, wsID, "OWNER")
	gotUser, gotServer, err := h.ensureDefaultComposioServer(req, wsID, "")
	if err != nil {
		t.Fatalf("ensureDefault: %v", err)
	}
	if gotUser != "user-a" || gotServer != "mcp_srv_1" {
		t.Errorf("ensureDefault = (%q,%q), want (user-a, mcp_srv_1)", gotUser, gotServer)
	}

	// Persisted on composio_settings.
	var du, ds string
	if err := db.QueryRow(`SELECT default_user_id, default_mcp_server_id FROM composio_settings WHERE workspace_id = ?`, wsID).
		Scan(&du, &ds); err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if du != "user-a" || ds != "mcp_srv_1" {
		t.Errorf("persisted defaults = (%q,%q)", du, ds)
	}

	// The managed-key credential was upserted so the resolver can attach it.
	var n string
	if err := db.QueryRow(`SELECT name FROM credentials WHERE workspace_id = ? AND name = ?`, wsID, composioManagedKeyName).
		Scan(&n); err != nil {
		t.Fatalf("managed credential missing: %v", err)
	}
}

func TestEnsureDefault_MultipleUsers_Errors(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t,
		`{"items":[{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`,
		`{"items":[
			{"id":"ca_1","user_id":"user-a","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_gm"}},
			{"id":"ca_2","user_id":"user-b","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_gm"}}
		]}`)

	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL, DefaultConnector: true,
	})

	req := withWorkspaceUser(httptest.NewRequest("PUT", "/", nil), userID, wsID, "OWNER")
	_, _, err := h.ensureDefaultComposioServer(req, wsID, "")
	if err == nil {
		t.Fatal("expected error for multiple connected users")
	}
	var de *composioDefaultError
	if !errors.As(err, &de) || de.status != http.StatusBadRequest {
		t.Fatalf("expected 400 composioDefaultError, got %v", err)
	}
	if !strings.Contains(err.Error(), "multiple") {
		t.Errorf("error = %q, want mention of multiple users", err.Error())
	}
}

func TestEnsureDefault_ZeroUsers_Errors(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL, DefaultConnector: true,
	})

	req := withWorkspaceUser(httptest.NewRequest("PUT", "/", nil), userID, wsID, "OWNER")
	_, _, err := h.ensureDefaultComposioServer(req, wsID, "")
	if err == nil {
		t.Fatal("expected error for zero connected users")
	}
	var de *composioDefaultError
	if !errors.As(err, &de) || de.status != http.StatusBadRequest {
		t.Fatalf("expected 400 composioDefaultError, got %v", err)
	}
	if !strings.Contains(err.Error(), "connect an account") {
		t.Errorf("error = %q, want 'connect an account first'", err.Error())
	}
}

// Explicit user override skips derivation (works even with multiple users).
func TestEnsureDefault_ExplicitUser_Overrides(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t,
		`{"items":[{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`,
		`{"items":[
			{"id":"ca_1","user_id":"user-a","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_gm"}},
			{"id":"ca_2","user_id":"user-b","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_gm"}}
		]}`)

	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL, DefaultConnector: true,
	})

	req := withWorkspaceUser(httptest.NewRequest("PUT", "/", nil), userID, wsID, "OWNER")
	gotUser, gotServer, err := h.ensureDefaultComposioServer(req, wsID, "user-b")
	if err != nil {
		t.Fatalf("ensureDefault with explicit user: %v", err)
	}
	if gotUser != "user-b" || gotServer != "mcp_srv_1" {
		t.Errorf("ensureDefault = (%q,%q), want (user-b, mcp_srv_1)", gotUser, gotServer)
	}
}
