package composio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeComposio spins up an httptest.Server that mimics the two v3 list
// endpoints the client calls, and records the x-api-key it received so the
// auth-header contract is asserted.
func fakeComposio(t *testing.T, authConfigs, connectedAccounts string) (*httptest.Server, *string) {
	t.Helper()
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/auth_configs":
			_, _ = w.Write([]byte(authConfigs))
		case "/api/v3/connected_accounts":
			_, _ = w.Write([]byte(connectedAccounts))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gotKey
}

func TestClient_ListAuthConfigs(t *testing.T) {
	srv, gotKey := fakeComposio(t,
		`{"items":[{"id":"ac_JE6J7fDSsneA","name":"gmail-i6p1sb","status":"ENABLED","toolkit":{"slug":"gmail","logo":"https://logos.composio.dev/api/gmail"}}]}`,
		`{"items":[]}`)
	c := NewClient("ak_test", srv.URL)

	got, err := c.ListAuthConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListAuthConfigs: %v", err)
	}
	if *gotKey != "ak_test" {
		t.Errorf("x-api-key header = %q, want ak_test", *gotKey)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != "ac_JE6J7fDSsneA" || got[0].Toolkit.Slug != "gmail" || got[0].Status != "ENABLED" {
		t.Errorf("unexpected auth config: %+v", got[0])
	}
}

func TestClient_ListConnectedAccounts(t *testing.T) {
	srv, _ := fakeComposio(t,
		`{"items":[]}`,
		`{"items":[{"id":"ca_2pjydr0oHqiI","user_id":"pg-test-1","status":"ACTIVE","toolkit":{"slug":"gmail"},"auth_config":{"id":"ac_JE6J7fDSsneA","auth_scheme":"OAUTH2","is_composio_managed":true,"is_disabled":false}}]}`)
	c := NewClient("ak_test", srv.URL)

	got, err := c.ListConnectedAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListConnectedAccounts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	acct := got[0]
	if acct.ID != "ca_2pjydr0oHqiI" || acct.UserID != "pg-test-1" || acct.Status != "ACTIVE" {
		t.Errorf("unexpected account: %+v", acct)
	}
	if acct.Toolkit.Slug != "gmail" || acct.AuthConfig.AuthScheme != "OAUTH2" || !acct.AuthConfig.IsComposioManaged {
		t.Errorf("unexpected embedded fields: %+v", acct)
	}
}

func TestClient_ListToolkits(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_items":1047,"items":[
			{"slug":"github","name":"GitHub","no_auth":false,"meta":{"description":"code hosting","logo":"https://logos.composio.dev/api/github","tools_count":846,"categories":[{"id":"developer-tools","name":"developer tools"}]}}
		]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("ak_test", srv.URL)

	page, err := c.ListToolkits(context.Background(), "git", "", 5)
	if err != nil {
		t.Fatalf("ListToolkits: %v", err)
	}
	if !strings.Contains(gotQuery, "search=git") || !strings.Contains(gotQuery, "limit=5") {
		t.Errorf("query = %q, want search=git & limit=5", gotQuery)
	}
	if page.TotalItems != 1047 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	tk := page.Items[0]
	if tk.Slug != "github" || tk.Name != "GitHub" || tk.Meta.ToolsCount != 846 || len(tk.Meta.Categories) != 1 {
		t.Errorf("unexpected toolkit: %+v", tk)
	}
}

func TestClient_ListTools(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_items":846,"items":[
			{"slug":"GITHUB_CREATE_AN_ISSUE","name":"Create an issue","description":"Create a new issue in a repository","toolkit":{"slug":"github","logo":"https://logos.composio.dev/api/github"}}
		]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("ak_test", srv.URL)

	page, err := c.ListTools(context.Background(), "github", "issue", 5)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if !strings.Contains(gotQuery, "toolkit_slug=github") || !strings.Contains(gotQuery, "search=issue") || !strings.Contains(gotQuery, "limit=5") {
		t.Errorf("query = %q, want toolkit_slug=github & search=issue & limit=5", gotQuery)
	}
	if page.TotalItems != 846 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	tool := page.Items[0]
	if tool.Slug != "GITHUB_CREATE_AN_ISSUE" || tool.Name != "Create an issue" || tool.Toolkit.Slug != "github" {
		t.Errorf("unexpected tool: %+v", tool)
	}
}

func TestClient_ListTriggerTypes(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_items":12,"items":[
			{"slug":"GMAIL_NEW_GMAIL_MESSAGE","name":"New Gmail message","description":"Triggers when a new email arrives","type":"poll","toolkit":{"slug":"gmail","logo":"https://logos.composio.dev/api/gmail"}}
		]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("ak_test", srv.URL)

	page, err := c.ListTriggerTypes(context.Background(), "gmail", "message", 5)
	if err != nil {
		t.Fatalf("ListTriggerTypes: %v", err)
	}
	if !strings.Contains(gotQuery, "toolkit_slugs=gmail") || !strings.Contains(gotQuery, "search=message") || !strings.Contains(gotQuery, "limit=5") {
		t.Errorf("query = %q, want toolkit_slugs=gmail & search=message & limit=5", gotQuery)
	}
	if page.TotalItems != 12 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	tt := page.Items[0]
	if tt.Slug != "GMAIL_NEW_GMAIL_MESSAGE" || tt.Name != "New Gmail message" || tt.Type != "poll" || tt.Toolkit.Slug != "gmail" {
		t.Errorf("unexpected trigger type: %+v", tt)
	}
}

func TestClient_CreateTriggerInstance(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trigger_id":"ti_abc123"}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("ak_test", srv.URL)

	inst, err := c.CreateTriggerInstance(context.Background(), "GMAIL_NEW_GMAIL_MESSAGE", "user-1", map[string]any{"interval": 60})
	if err != nil {
		t.Fatalf("CreateTriggerInstance: %v", err)
	}
	if gotPath != "/api/v3.1/trigger_instances/GMAIL_NEW_GMAIL_MESSAGE/upsert" {
		t.Errorf("path = %q, want .../GMAIL_NEW_GMAIL_MESSAGE/upsert", gotPath)
	}
	if gotBody["user_id"] != "user-1" {
		t.Errorf("body user_id = %v, want user-1", gotBody["user_id"])
	}
	if cfg, ok := gotBody["trigger_config"].(map[string]any); !ok || cfg["interval"] != float64(60) {
		t.Errorf("body trigger_config = %v, want {interval:60}", gotBody["trigger_config"])
	}
	if inst.ID != "ti_abc123" || inst.TriggerName != "GMAIL_NEW_GMAIL_MESSAGE" || inst.UserID != "user-1" {
		t.Errorf("unexpected instance: %+v", inst)
	}
}

func TestClient_ConnectedAccountManagement(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		// Revoke/delete return no useful body; 204 exercises the no-decode path.
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("ak_test", srv.URL)

	if err := c.RevokeConnectedAccount(context.Background(), "ca_2pjydr0oHqiI"); err != nil {
		t.Fatalf("RevokeConnectedAccount: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v3.1/connected_accounts/ca_2pjydr0oHqiI/revoke" {
		t.Errorf("revoke = %s %s, want POST .../ca_2pjydr0oHqiI/revoke", gotMethod, gotPath)
	}

	if err := c.RefreshConnectedAccount(context.Background(), "ca_2pjydr0oHqiI"); err != nil {
		t.Fatalf("RefreshConnectedAccount: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v3.1/connected_accounts/ca_2pjydr0oHqiI/refresh" {
		t.Errorf("refresh = %s %s, want POST .../ca_2pjydr0oHqiI/refresh", gotMethod, gotPath)
	}

	if err := c.DeleteConnectedAccount(context.Background(), "ca_2pjydr0oHqiI"); err != nil {
		t.Fatalf("DeleteConnectedAccount: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v3.1/connected_accounts/ca_2pjydr0oHqiI" {
		t.Errorf("delete = %s %s, want DELETE .../ca_2pjydr0oHqiI", gotMethod, gotPath)
	}
}

func TestClient_ListMCPServers(t *testing.T) {
	var gotMethod, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"id":"mcp_srv_1","name":"crewship-abc","mcp_url":"https://mcp.composio.dev/server/mcp_srv_1"}
		]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("ak_test", srv.URL)

	got, err := c.ListMCPServers(context.Background(), "crewship-abc")
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", gotMethod)
	}
	if !strings.Contains(gotQuery, "name=crewship-abc") {
		t.Errorf("query = %q, want name=crewship-abc", gotQuery)
	}
	if len(got) != 1 || got[0].ID != "mcp_srv_1" || got[0].Name != "crewship-abc" ||
		got[0].MCPURL != "https://mcp.composio.dev/server/mcp_srv_1" {
		t.Errorf("unexpected servers: %+v", got)
	}
}

func TestClient_DefaultBaseURL(t *testing.T) {
	c := NewClient("ak_test", "")
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	// trailing slash trimmed
	c2 := NewClient("ak_test", "https://example.com/")
	if c2.baseURL != "https://example.com" {
		t.Errorf("baseURL = %q, want trimmed", c2.baseURL)
	}
}

func TestClient_NonOKStatusSurfacesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("bad", srv.URL)

	_, err := c.ListAuthConfigs(context.Background())
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}
