package composio

import (
	"context"
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
