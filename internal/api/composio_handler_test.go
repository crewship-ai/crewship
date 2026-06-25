package api

import (
	"bytes"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func newComposioTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeComposioAPI mimics the two Composio v3 list endpoints.
func fakeComposioAPI(t *testing.T, authConfigs, connectedAccounts string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/auth_configs":
			_, _ = w.Write([]byte(authConfigs))
		case "/api/v3/connected_accounts":
			_, _ = w.Write([]byte(connectedAccounts))
		case "/api/v3/toolkits":
			_, _ = w.Write([]byte(`{"total_items":1047,"items":[{"slug":"github","name":"GitHub","meta":{"description":"x","logo":"l","tools_count":846,"categories":[{"id":"developer-tools","name":"developer tools"}]}}]}`))
		case "/api/v3.1/tools":
			_, _ = w.Write([]byte(`{"total_items":846,"items":[{"slug":"GITHUB_CREATE_AN_ISSUE","name":"Create an issue","description":"Create a new issue","toolkit":{"slug":"github"}}]}`))
		case "/api/v3.1/triggers_types":
			_, _ = w.Write([]byte(`{"total_items":12,"items":[{"slug":"GMAIL_NEW_GMAIL_MESSAGE","name":"New Gmail message","description":"New email arrives","type":"poll","toolkit":{"slug":"gmail"}}]}`))
		case "/api/v3.1/trigger_instances/active":
			_, _ = w.Write([]byte(`{"items":[{"id":"ti_1","trigger_name":"GMAIL_NEW_GMAIL_MESSAGE","user_id":"user-1","connected_account_id":"ca_1","trigger_config":{"interval":60}}]}`))
		case "/api/v3.1/trigger_instances/GMAIL_NEW_GMAIL_MESSAGE/upsert":
			_, _ = w.Write([]byte(`{"trigger_id":"ti_new"}`))
		case "/api/v3.1/auth_configs":
			_, _ = w.Write([]byte(`{"id":"ac_new"}`))
		case "/api/v3.1/connected_accounts/link":
			_, _ = w.Write([]byte(`{"link_token":"lt_1","redirect_url":"https://oauth.example/authorize?x=1","connected_account_id":"ca_new"}`))
		case "/api/v3.1/mcp/servers":
			// Mirror Composio's create-server response: an id + a base mcp_url
			// the handler scopes per-user with a `?user_id=` suffix.
			_, _ = w.Write([]byte(`{"id":"mcp_srv_1","mcp_url":"https://mcp.composio.dev/server/mcp_srv_1"}`))
		// Connected-account management: revoke/refresh (POST) + delete (DELETE).
		// Composio returns no useful body; 204 exercises the client's no-decode path.
		case "/api/v3.1/connected_accounts/ca_1/revoke",
			"/api/v3.1/connected_accounts/ca_1/refresh",
			"/api/v3.1/connected_accounts/ca_1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestComposio_ListInventory_Enabled(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t,
		`{"items":[{"id":"ac_JE6J7fDSsneA","name":"gmail-i6p1sb","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`,
		`{"items":[
			{"id":"ca_2","user_id":"user-b","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_JE6J7fDSsneA","auth_scheme":"OAUTH2","is_composio_managed":true}},
			{"id":"ca_1","user_id":"user-a","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_JE6J7fDSsneA","auth_scheme":"OAUTH2","is_composio_managed":true}}
		]}`)

	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/inventory", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListInventory(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got composioInventoryResponse
	mustUnmarshal(t, rr, &got)

	if !got.Enabled {
		t.Error("expected enabled=true")
	}
	if len(got.AuthConfigs) != 1 || got.AuthConfigs[0].Toolkit.Slug != "gmail" {
		t.Errorf("auth configs = %+v", got.AuthConfigs)
	}
	// Two distinct user_ids → two buckets, sorted (user-a before user-b).
	if len(got.Users) != 2 {
		t.Fatalf("users = %d, want 2 (%+v)", len(got.Users), got.Users)
	}
	if got.Users[0].UserID != "user-a" || got.Users[1].UserID != "user-b" {
		t.Errorf("users not sorted by user_id: %+v", got.Users)
	}
	if len(got.Users[0].ConnectedAccounts) != 1 || got.Users[0].ConnectedAccounts[0].ID != "ca_1" {
		t.Errorf("user-a accounts = %+v", got.Users[0].ConnectedAccounts)
	}
}

func TestComposio_ListToolkits(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/toolkits?search=git", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListToolkits(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got composioToolkitsResponse
	mustUnmarshal(t, rr, &got)
	if !got.Enabled || got.Total != 1047 || len(got.Toolkits) != 1 || got.Toolkits[0].Slug != "github" {
		t.Errorf("unexpected toolkits response: %+v", got)
	}
}

func TestComposio_ListTools(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/tools?toolkit=github", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListTools(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got composioToolsResponse
	mustUnmarshal(t, rr, &got)
	if !got.Enabled || got.Total != 846 || len(got.Tools) != 1 || got.Tools[0].Slug != "GITHUB_CREATE_AN_ISSUE" {
		t.Errorf("unexpected tools response: %+v", got)
	}
	if got.Tools[0].Toolkit.Slug != "github" {
		t.Errorf("tool toolkit = %+v", got.Tools[0].Toolkit)
	}
}

func TestComposio_ListTools_RequiresToolkit(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/tools", nil) // no toolkit
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListTools(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (toolkit required)", rr.Code)
	}
}

func TestComposio_ListTriggerTypes(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/triggers?toolkit=gmail", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListTriggerTypes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got composioTriggerTypesResponse
	mustUnmarshal(t, rr, &got)
	if !got.Enabled || got.Total != 12 || len(got.Triggers) != 1 || got.Triggers[0].Slug != "GMAIL_NEW_GMAIL_MESSAGE" {
		t.Errorf("unexpected trigger types response: %+v", got)
	}
	if got.Triggers[0].Type != "poll" || got.Triggers[0].Toolkit.Slug != "gmail" {
		t.Errorf("unexpected trigger type fields: %+v", got.Triggers[0])
	}
}

func TestComposio_ListActiveTriggers(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/triggers/active", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListActiveTriggers(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got composioActiveTriggersResponse
	mustUnmarshal(t, rr, &got)
	if !got.Enabled || len(got.Triggers) != 1 || got.Triggers[0].ID != "ti_1" {
		t.Errorf("unexpected active triggers response: %+v", got)
	}
	if got.Triggers[0].TriggerName != "GMAIL_NEW_GMAIL_MESSAGE" || got.Triggers[0].UserID != "user-1" {
		t.Errorf("unexpected active trigger fields: %+v", got.Triggers[0])
	}
}

func TestComposio_CreateTrigger(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	body := bytes.NewBufferString(`{"slug":"GMAIL_NEW_GMAIL_MESSAGE","user_id":"user-1","config":{"interval":60}}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/integrations/composio/triggers", body), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateTrigger(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got composioCreateTriggerResponse
	mustUnmarshal(t, rr, &got)
	if !got.Enabled || got.Trigger.ID != "ti_new" || got.Trigger.TriggerName != "GMAIL_NEW_GMAIL_MESSAGE" || got.Trigger.UserID != "user-1" {
		t.Errorf("unexpected create trigger resp: %+v", got)
	}
}

func TestComposio_CreateTrigger_RequiresFields(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{Enabled: true, APIKey: "k", BaseURL: srv.URL})

	body := bytes.NewBufferString(`{"slug":"GMAIL_NEW_GMAIL_MESSAGE"}`) // missing user_id
	req := withWorkspaceUser(httptest.NewRequest("POST", "/t", body), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateTrigger(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
}

func TestComposio_Settings_UpsertAndUse(t *testing.T) {
	// 32-byte AES key (hex) so encryption.Encrypt works in the test env.
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	// nil env config → the provider is ONLY configurable via the stored key.
	h := NewComposioHandler(db, newComposioTestLogger(), nil)

	// PUT a key (validated against the fake toolkits endpoint).
	body := bytes.NewBufferString(`{"api_key":"ak_ws","base_url":"` + srv.URL + `","label":"Proj"}`)
	req := withWorkspaceUser(httptest.NewRequest("PUT", "/api/v1/integrations/composio/settings", body), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpsertSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("upsert status=%d body=%s", rr.Code, rr.Body.String())
	}
	var put composioSettingsResponse
	mustUnmarshal(t, rr, &put)
	if !put.Configured || put.Source != "workspace" || put.Label != "Proj" {
		t.Fatalf("upsert resp=%+v", put)
	}

	// GET reflects the workspace source.
	grr := httptest.NewRecorder()
	h.GetSettings(grr, withWorkspaceUser(httptest.NewRequest("GET", "/s", nil), userID, wsID, "VIEWER"))
	var got composioSettingsResponse
	mustUnmarshal(t, grr, &got)
	if got.Source != "workspace" {
		t.Errorf("GetSettings source=%s, want workspace", got.Source)
	}

	// Inventory now resolves the stored key (no env fallback present).
	irr := httptest.NewRecorder()
	h.ListInventory(irr, withWorkspaceUser(httptest.NewRequest("GET", "/i", nil), userID, wsID, "VIEWER"))
	var inv composioInventoryResponse
	mustUnmarshal(t, irr, &inv)
	if !inv.Enabled {
		t.Errorf("expected enabled via stored workspace key, got %+v", inv)
	}
}

func TestComposio_Settings_InvalidKeyRejected(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(bad.Close)

	h := NewComposioHandler(db, newComposioTestLogger(), nil)
	body := bytes.NewBufferString(`{"api_key":"nope","base_url":"` + bad.URL + `"}`)
	req := withWorkspaceUser(httptest.NewRequest("PUT", "/api/v1/integrations/composio/settings", body), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UpsertSettings(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (invalid key rejected)", rr.Code)
	}
}

func TestComposio_Connect(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// auth_configs GET returns only gmail → connecting github must create a new
	// managed auth config, then a connect link.
	srv := fakeComposioAPI(t,
		`{"items":[{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`,
		`{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	body := bytes.NewBufferString(`{"toolkit":"github","user_id":"user-1"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/integrations/composio/connect", body), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Connect(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got composioConnectResponse
	mustUnmarshal(t, rr, &got)
	if got.RedirectURL != "https://oauth.example/authorize?x=1" || got.ConnectedAccountID != "ca_new" || got.UserID != "user-1" {
		t.Errorf("unexpected connect resp: %+v", got)
	}
}

func TestComposio_Connect_RequiresFields(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{Enabled: true, APIKey: "k", BaseURL: srv.URL})

	body := bytes.NewBufferString(`{"toolkit":"github"}`) // missing user_id
	req := withWorkspaceUser(httptest.NewRequest("POST", "/c", body), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Connect(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
}

func TestComposio_BindAgent_PersistsRows(t *testing.T) {
	// Encrypting the managed Composio key requires a 32-byte AES key (hex).
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Seed an agent in the workspace (the bind validates ownership).
	agentID := "agent-bind-1"
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'Binder', 'binder')`, agentID, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	srv := fakeComposioAPI(t,
		`{"items":[{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`,
		`{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	body := bytes.NewBufferString(`{"user_id":"user-42"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/integrations/composio/agents/"+agentID+"/bind", body), userID, wsID, "OWNER")
	req.SetPathValue("agentId", agentID)
	rr := httptest.NewRecorder()
	h.BindAgent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("bind status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got composioBindResponse
	mustUnmarshal(t, rr, &got)
	if got.AgentID != agentID || got.UserID != "user-42" {
		t.Errorf("unexpected bind resp: %+v", got)
	}
	wantEndpoint := "https://mcp.composio.dev/server/mcp_srv_1?user_id=user-42"
	if got.Endpoint != wantEndpoint {
		t.Errorf("endpoint = %q, want %q", got.Endpoint, wantEndpoint)
	}

	// workspace_mcp_servers row exists, scoped to the user, streamable-http.
	var srvName, transport, endpoint, icon string
	var enabled int
	if err := db.QueryRow(`SELECT name, transport, endpoint, icon, enabled
		FROM workspace_mcp_servers WHERE workspace_id = ? AND name = 'composio-user-42'`, wsID).
		Scan(&srvName, &transport, &endpoint, &icon, &enabled); err != nil {
		t.Fatalf("query workspace_mcp_servers: %v", err)
	}
	if transport != "streamable-http" || endpoint != wantEndpoint || icon != "composio" || enabled != 1 {
		t.Errorf("ws server row = name=%s transport=%s endpoint=%s icon=%s enabled=%d",
			srvName, transport, endpoint, icon, enabled)
	}

	// agent_mcp_bindings row exists with the api_key/x-api-key cred shape.
	var credType, credHeader, scope string
	var credID sql.NullString
	if err := db.QueryRow(`SELECT cred_type, cred_header, mcp_server_scope, credential_id
		FROM agent_mcp_bindings WHERE agent_id = ?`, agentID).
		Scan(&credType, &credHeader, &scope, &credID); err != nil {
		t.Fatalf("query agent_mcp_bindings: %v", err)
	}
	if credType != "api_key" || credHeader != "x-api-key" || scope != "workspace" || !credID.Valid {
		t.Errorf("binding row = cred_type=%s cred_header=%s scope=%s cred=%v", credType, credHeader, scope, credID)
	}

	// The referenced credential holds the (encrypted) Composio key.
	var credName, credKind string
	if err := db.QueryRow(`SELECT name, type FROM credentials WHERE id = ?`, credID.String).
		Scan(&credName, &credKind); err != nil {
		t.Fatalf("query credentials: %v", err)
	}
	if credName != composioManagedKeyName || credKind != "API_KEY" {
		t.Errorf("credential row = name=%s type=%s", credName, credKind)
	}

	// Re-binding the same user is idempotent (upserts, no duplicate rows).
	body2 := bytes.NewBufferString(`{"user_id":"user-42"}`)
	req2 := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/integrations/composio/agents/"+agentID+"/bind", body2), userID, wsID, "OWNER")
	req2.SetPathValue("agentId", agentID)
	rr2 := httptest.NewRecorder()
	h.BindAgent(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("re-bind status=%d body=%s", rr2.Code, rr2.Body.String())
	}
	var serverCount, bindCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id = ? AND name = 'composio-user-42'`, wsID).Scan(&serverCount); err != nil {
		t.Fatalf("count servers: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE agent_id = ?`, agentID).Scan(&bindCount); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if serverCount != 1 || bindCount != 1 {
		t.Errorf("after re-bind: servers=%d bindings=%d, want 1/1", serverCount, bindCount)
	}
}

func TestComposio_BindAgent_RejectsForeignAgent(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t,
		`{"items":[{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`,
		`{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	body := bytes.NewBufferString(`{"user_id":"user-42"}`)
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/integrations/composio/agents/ghost/bind", body), userID, wsID, "OWNER")
	req.SetPathValue("agentId", "ghost")
	rr := httptest.NewRecorder()
	h.BindAgent(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (foreign/unknown agent)", rr.Code)
	}
}

func TestComposio_BindAgent_RequiresUserID(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{Enabled: true, APIKey: "k", BaseURL: srv.URL})

	body := bytes.NewBufferString(`{}`) // missing user_id
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/integrations/composio/agents/a/bind", body), userID, wsID, "OWNER")
	req.SetPathValue("agentId", "a")
	rr := httptest.NewRecorder()
	h.BindAgent(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
}

func TestComposio_RevokeAccount(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/integrations/composio/accounts/ca_1/revoke", nil), userID, wsID, "OWNER")
	req.SetPathValue("accountId", "ca_1")
	rr := httptest.NewRecorder()
	h.RevokeAccount(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s, want 204", rr.Code, rr.Body.String())
	}
}

func TestComposio_DeleteAccount(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: srv.URL,
	})

	req := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/integrations/composio/accounts/ca_1", nil), userID, wsID, "OWNER")
	req.SetPathValue("accountId", "ca_1")
	rr := httptest.NewRecorder()
	h.DeleteAccount(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s, want 204", rr.Code, rr.Body.String())
	}
}

func TestComposio_RevokeAccount_NotConfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// nil config → provider unconfigured: management ops 400 rather than proxying.
	h := NewComposioHandler(db, newComposioTestLogger(), nil)

	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/integrations/composio/accounts/ca_1/revoke", nil), userID, wsID, "OWNER")
	req.SetPathValue("accountId", "ca_1")
	rr := httptest.NewRecorder()
	h.RevokeAccount(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (Composio not configured)", rr.Code)
	}
}

func TestComposio_ListInventory_Disabled(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// nil config → provider disabled.
	h := NewComposioHandler(db, newComposioTestLogger(), nil)

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/inventory", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListInventory(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got composioInventoryResponse
	mustUnmarshal(t, rr, &got)
	if got.Enabled {
		t.Error("expected enabled=false when unconfigured")
	}
	if len(got.AuthConfigs) != 0 || len(got.Users) != 0 {
		t.Errorf("expected empty payload, got %+v", got)
	}
}

func TestComposio_ListInventory_UpstreamError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "bad", BaseURL: srv.URL,
	})

	req := httptest.NewRequest("GET", "/api/v1/integrations/composio/inventory", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.ListInventory(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}
