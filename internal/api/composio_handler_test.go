package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
)

func newComposioTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// armTestEncryptionKey sets a deterministic 32-byte AES key (hex) for tests
// that exercise credential encryption. Built at runtime via strings.Repeat so
// the key isn't a committed string literal (keeps gitleaks quiet — it's a
// throwaway test key, not a secret).
func armTestEncryptionKey(t *testing.T) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("0123456789abcdef", 4))
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
			// Branch on toolkit_slug so gmail returns a read+write mix (the bind
			// read/custom paths resolve their allowed_tools from here) while github
			// keeps the single tool the catalog tests assert. No next_cursor ⇒
			// ListAllTools stops after one page.
			switch r.URL.Query().Get("toolkit_slug") {
			case "gmail":
				_, _ = w.Write([]byte(`{"total_items":4,"items":[
					{"slug":"GMAIL_FETCH_EMAILS","name":"Fetch emails","description":"read","toolkit":{"slug":"gmail"}},
					{"slug":"GMAIL_LIST_THREADS","name":"List threads","description":"read","toolkit":{"slug":"gmail"}},
					{"slug":"GMAIL_SEND_EMAIL","name":"Send email","description":"write","toolkit":{"slug":"gmail"}},
					{"slug":"GMAIL_CREATE_EMAIL_DRAFT","name":"Create draft","description":"write","toolkit":{"slug":"gmail"}}
				]}`))
			default:
				_, _ = w.Write([]byte(`{"total_items":846,"items":[{"slug":"GITHUB_CREATE_AN_ISSUE","name":"Create an issue","description":"Create a new issue","toolkit":{"slug":"github"}}]}`))
			}
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
			// find-or-create: GET lists existing servers (none here, so the
			// handler provisions one); POST mirrors Composio's create-server
			// response — an id + a base mcp_url the handler scopes per-user.
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`{"items":[]}`))
			case http.MethodPost:
				_, _ = w.Write([]byte(`{"id":"mcp_srv_1","mcp_url":"https://mcp.composio.dev/server/mcp_srv_1"}`))
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		// Connected-account management: revoke/refresh (POST) + delete (DELETE).
		// Branch on method so a wrong-verb regression fails instead of silently
		// 204-ing. Composio returns no useful body; 204 exercises the no-decode path.
		case "/api/v3.1/connected_accounts/ca_1/revoke",
			"/api/v3.1/connected_accounts/ca_1/refresh":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v3.1/connected_accounts/ca_1":
			if r.Method != http.MethodDelete {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
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
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	srv := fakeComposioAPI(t, `{"items":[]}`, `{"items":[]}`)
	// base_url is no longer client-supplied: the validation probe and the
	// resolved client both honour only the server env base URL, so point that
	// at the fake server. The stored workspace key is still what gets persisted
	// and used (source: "workspace").
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_env", BaseURL: srv.URL,
	})

	// PUT a key (validated against the fake toolkits endpoint). No base_url:
	// the API rejects/ignores it now (SSRF guard).
	body := bytes.NewBufferString(`{"api_key":"ak_ws","label":"Proj"}`)
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

	// Probe host is env-only now; point it at the bad server so the key
	// validation hits the 401 (base_url in the body would be ignored).
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_env", BaseURL: bad.URL,
	})
	body := bytes.NewBufferString(`{"api_key":"nope"}`)
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
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Seed an agent in the workspace (the bind validates ownership).
	agentID := "agent-bind-1"
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'Binder', 'binder')`, agentID, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	// Stateful fake: find-or-create. GET /mcp/servers lists what POST created
	// (echoing the ?name= filter so the handler can match by name); mcpCreates
	// counts provisioning calls so re-binding can assert "created exactly once".
	var mu sync.Mutex
	mcpCreates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v3/auth_configs" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}}]}`))
		case r.URL.Path == "/api/v3.1/mcp/servers" && r.Method == http.MethodGet:
			mu.Lock()
			created := mcpCreates
			mu.Unlock()
			if created > 0 {
				name := r.URL.Query().Get("name")
				_, _ = w.Write([]byte(fmt.Sprintf(`{"items":[{"id":"mcp_srv_1","name":%q,"mcp_url":"https://mcp.composio.dev/server/mcp_srv_1"}]}`, name)))
			} else {
				_, _ = w.Write([]byte(`{"items":[]}`))
			}
		case r.URL.Path == "/api/v3.1/mcp/servers" && r.Method == http.MethodPost:
			mu.Lock()
			mcpCreates++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"mcp_srv_1","mcp_url":"https://mcp.composio.dev/server/mcp_srv_1"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
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
	// Empty body ⇒ every connected app (here just gmail) bound at full scope.
	if len(got.Apps) != 1 || got.Apps[0].Toolkit != "gmail" || got.Apps[0].Mode != "full" {
		t.Fatalf("unexpected apps in bind resp: %+v", got.Apps)
	}
	// Endpoint is the per-user MCP *transport* URL (…/v3/mcp/<serverID>/mcp),
	// built from the client's base host — not the canonical mcp_url.
	wantEndpoint := srv.URL + "/v3/mcp/mcp_srv_1/mcp?user_id=user-42"
	if got.Apps[0].Endpoint != wantEndpoint {
		t.Errorf("endpoint = %q, want %q", got.Apps[0].Endpoint, wantEndpoint)
	}

	// workspace_mcp_servers row exists PER (agent, app): composio-<agentID>-gmail,
	// streamable-http.
	rowName := "composio-" + agentID + "-gmail"
	var srvName, transport, endpoint, icon string
	var enabled int
	if err := db.QueryRow(`SELECT name, transport, endpoint, icon, enabled
		FROM workspace_mcp_servers WHERE workspace_id = ? AND name = ?`, wsID, rowName).
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
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id = ? AND name = ?`, wsID, rowName).Scan(&serverCount); err != nil {
		t.Fatalf("count servers: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings WHERE agent_id = ?`, agentID).Scan(&bindCount); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if serverCount != 1 || bindCount != 1 {
		t.Errorf("after re-bind: servers=%d bindings=%d, want 1/1", serverCount, bindCount)
	}

	// Find-or-create: the Composio MCP server must be provisioned exactly ONCE
	// across both binds — the second bind reuses the existing server's mcp_url.
	mu.Lock()
	creates := mcpCreates
	mu.Unlock()
	if creates != 1 {
		t.Errorf("Composio MCP server created %d times, want exactly 1 (find-or-create)", creates)
	}
}

func TestComposio_BindAgent_RejectsForeignAgent(t *testing.T) {
	armTestEncryptionKey(t)
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

// composioBindFake is a stateful Composio fake for the per-app bind tests. It
// serves auth configs + gmail/github tools, implements find-or-create for MCP
// servers, and records the allowed_tools each create POST received (keyed by
// server name) so tests can assert how a mode maps to allowed_tools.
type composioBindFake struct {
	srv           *httptest.Server
	mu            sync.Mutex
	creates       int
	allowedByName map[string][]string // server name → allowed_tools posted
	idByName      map[string]string   // server name → assigned id (find-or-create)
}

func newComposioBindFake(t *testing.T, authConfigs string) *composioBindFake {
	t.Helper()
	f := &composioBindFake{allowedByName: map[string][]string{}, idByName: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v3/auth_configs" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(authConfigs))
		case r.URL.Path == "/api/v3.1/tools" && r.Method == http.MethodGet:
			switch r.URL.Query().Get("toolkit_slug") {
			case "gmail":
				_, _ = w.Write([]byte(`{"items":[
					{"slug":"GMAIL_FETCH_EMAILS","name":"Fetch","description":"r","toolkit":{"slug":"gmail"}},
					{"slug":"GMAIL_LIST_THREADS","name":"List","description":"r","toolkit":{"slug":"gmail"}},
					{"slug":"GMAIL_SEND_EMAIL","name":"Send","description":"w","toolkit":{"slug":"gmail"}},
					{"slug":"GMAIL_CREATE_EMAIL_DRAFT","name":"Draft","description":"w","toolkit":{"slug":"gmail"}}
				]}`))
			default:
				_, _ = w.Write([]byte(`{"items":[{"slug":"GITHUB_CREATE_AN_ISSUE","name":"Issue","description":"w","toolkit":{"slug":"github"}}]}`))
			}
		case r.URL.Path == "/api/v3.1/mcp/servers" && r.Method == http.MethodGet:
			name := r.URL.Query().Get("name")
			f.mu.Lock()
			id := f.idByName[name]
			f.mu.Unlock()
			if id == "" {
				_, _ = w.Write([]byte(`{"items":[]}`))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"items":[{"id":%q,"name":%q,"mcp_url":"https://mcp.composio.dev/server/%s"}]}`, id, name, id)))
		case r.URL.Path == "/api/v3.1/mcp/servers" && r.Method == http.MethodPost:
			var body struct {
				Name         string   `json:"name"`
				AllowedTools []string `json:"allowed_tools"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.creates++
			id := fmt.Sprintf("mcp_srv_%d", f.creates)
			f.idByName[body.Name] = id
			f.allowedByName[body.Name] = body.AllowedTools
			f.mu.Unlock()
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"mcp_url":"https://mcp.composio.dev/server/%s"}`, id, id)))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// allowed returns the allowed_tools recorded for a server name (the toolkit's
// scope tag is part of the name; tests look it up by the composioServerName the
// handler would compute).
func (f *composioBindFake) allowed(name string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.allowedByName[name]
	return v, ok
}

// gmailGithubAuthConfigs is the auth-config catalog the per-app bind tests use.
const gmailGithubAuthConfigs = `{"items":[
	{"id":"ac_gm","name":"gmail-x","status":"ENABLED","toolkit":{"slug":"gmail"}},
	{"id":"ac_gh","name":"github-x","status":"ENABLED","toolkit":{"slug":"github"}}
]}`

func seedComposioBindAgent(t *testing.T, db *sql.DB, wsID, agentID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'Binder', ?)`, agentID, wsID, agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
}

func TestComposio_BindAgent_PerAppScopes(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := "agent-perapp"
	seedComposioBindAgent(t, db, wsID, agentID)

	fake := newComposioBindFake(t, gmailGithubAuthConfigs)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: fake.srv.URL,
	})

	// gmail at read scope + github at full scope, in ONE bind.
	body := bytes.NewBufferString(`{"user_id":"user-7","apps":[{"toolkit":"gmail","mode":"read"},{"toolkit":"github","mode":"full"}]}`)
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
	if len(got.Apps) != 2 {
		t.Fatalf("apps = %+v, want 2", got.Apps)
	}

	// Two per-app workspace_mcp_servers rows.
	for _, tk := range []string{"gmail", "github"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id=? AND name=?`, wsID, "composio-"+agentID+"-"+tk).Scan(&n); err != nil {
			t.Fatalf("count %s row: %v", tk, err)
		}
		if n != 1 {
			t.Errorf("server row for %s = %d, want 1", tk, n)
		}
	}

	// read mode → only the read gmail tools are passed as allowed_tools.
	gmailName := composioServerName(wsID, "gmail", composioScopeTag([]string{"GMAIL_FETCH_EMAILS", "GMAIL_LIST_THREADS"}))
	gmailAllowed, ok := fake.allowed(gmailName)
	if !ok {
		t.Fatalf("no create recorded for gmail server %q (recorded: %v)", gmailName, fake.allowedByName)
	}
	if len(gmailAllowed) != 2 {
		t.Fatalf("gmail allowed_tools = %v, want the 2 read tools", gmailAllowed)
	}
	for _, s := range gmailAllowed {
		if s != "GMAIL_FETCH_EMAILS" && s != "GMAIL_LIST_THREADS" {
			t.Errorf("gmail read scope leaked non-read tool %q (allowed=%v)", s, gmailAllowed)
		}
	}

	// full mode → allowed_tools empty (every tool).
	githubName := composioServerName(wsID, "github", "full")
	githubAllowed, ok := fake.allowed(githubName)
	if !ok {
		t.Fatalf("no create recorded for github server %q (recorded: %v)", githubName, fake.allowedByName)
	}
	if len(githubAllowed) != 0 {
		t.Errorf("github full scope allowed_tools = %v, want empty (all tools)", githubAllowed)
	}
}

func TestComposio_BindAgent_CustomScope(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := "agent-custom"
	seedComposioBindAgent(t, db, wsID, agentID)

	fake := newComposioBindFake(t, gmailGithubAuthConfigs)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: fake.srv.URL,
	})

	// custom: one real tool + one bogus slug (must be rejected, not over-granted).
	body := bytes.NewBufferString(`{"user_id":"user-7","apps":[{"toolkit":"gmail","mode":"custom","tools":["GMAIL_SEND_EMAIL","NOT_A_REAL_TOOL"]}]}`)
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/integrations/composio/agents/"+agentID+"/bind", body), userID, wsID, "OWNER")
	req.SetPathValue("agentId", agentID)
	rr := httptest.NewRecorder()
	h.BindAgent(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bind status=%d body=%s", rr.Code, rr.Body.String())
	}

	name := composioServerName(wsID, "gmail", composioScopeTag([]string{"GMAIL_SEND_EMAIL"}))
	allowed, ok := fake.allowed(name)
	if !ok {
		t.Fatalf("no create recorded for %q (recorded: %v)", name, fake.allowedByName)
	}
	if len(allowed) != 1 || allowed[0] != "GMAIL_SEND_EMAIL" {
		t.Errorf("custom allowed_tools = %v, want exactly [GMAIL_SEND_EMAIL] (bogus slug dropped)", allowed)
	}
}

func TestComposio_BindAgent_ReadNoToolsRejected(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := "agent-noread"
	seedComposioBindAgent(t, db, wsID, agentID)

	// github's only tool (GITHUB_CREATE_AN_ISSUE) is not a read tool, so a read
	// scope on github resolves to an empty set → 400.
	fake := newComposioBindFake(t, gmailGithubAuthConfigs)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: fake.srv.URL,
	})

	body := bytes.NewBufferString(`{"user_id":"user-7","apps":[{"toolkit":"github","mode":"read"}]}`)
	req := withWorkspaceUser(httptest.NewRequest("POST",
		"/api/v1/integrations/composio/agents/"+agentID+"/bind", body), userID, wsID, "OWNER")
	req.SetPathValue("agentId", agentID)
	rr := httptest.NewRecorder()
	h.BindAgent(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (no read tools for github)", rr.Code)
	}
}

func TestComposio_BindAgent_ReBindDropsApp(t *testing.T) {
	armTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	agentID := "agent-rebind"
	seedComposioBindAgent(t, db, wsID, agentID)

	fake := newComposioBindFake(t, gmailGithubAuthConfigs)
	h := NewComposioHandler(db, newComposioTestLogger(), &config.ComposioConfig{
		Enabled: true, APIKey: "ak_test", BaseURL: fake.srv.URL,
	})

	bind := func(jsonBody string) {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest("POST",
			"/api/v1/integrations/composio/agents/"+agentID+"/bind", bytes.NewBufferString(jsonBody)), userID, wsID, "OWNER")
		req.SetPathValue("agentId", agentID)
		rr := httptest.NewRecorder()
		h.BindAgent(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("bind status=%d body=%s", rr.Code, rr.Body.String())
		}
	}

	// First: gmail + github. Then: gmail only → github row + binding removed.
	bind(`{"user_id":"user-7","apps":[{"toolkit":"gmail","mode":"full"},{"toolkit":"github","mode":"full"}]}`)
	bind(`{"user_id":"user-7","apps":[{"toolkit":"gmail","mode":"full"}]}`)

	var ghServers, ghBindings, gmServers int
	githubRow := "composio-" + agentID + "-github"
	gmailRow := "composio-" + agentID + "-gmail"
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id=? AND name=?`, wsID, githubRow).Scan(&ghServers); err != nil {
		t.Fatalf("count github servers: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_mcp_bindings b JOIN workspace_mcp_servers ws ON ws.id=b.mcp_server_id WHERE b.agent_id=? AND ws.name=?`, agentID, githubRow).Scan(&ghBindings); err != nil {
		t.Fatalf("count github bindings: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id=? AND name=?`, wsID, gmailRow).Scan(&gmServers); err != nil {
		t.Fatalf("count gmail servers: %v", err)
	}
	if ghServers != 0 || ghBindings != 0 {
		t.Errorf("after re-bind dropping github: servers=%d bindings=%d, want 0/0", ghServers, ghBindings)
	}
	if gmServers != 1 {
		t.Errorf("gmail server after re-bind = %d, want 1 (kept)", gmServers)
	}

	// Unbind one app (gmail) leaves nothing.
	ureq := withWorkspaceUser(httptest.NewRequest("DELETE",
		"/api/v1/integrations/composio/agents/"+agentID+"/bind?toolkit=gmail", nil), userID, wsID, "OWNER")
	ureq.SetPathValue("agentId", agentID)
	urr := httptest.NewRecorder()
	h.UnbindAgent(urr, ureq)
	if urr.Code != http.StatusOK {
		t.Fatalf("unbind status=%d body=%s", urr.Code, urr.Body.String())
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_mcp_servers WHERE workspace_id=? AND icon='composio' AND name LIKE ?`, wsID, "composio-"+agentID+"-%").Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Errorf("after unbind gmail: remaining composio rows=%d, want 0", remaining)
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
