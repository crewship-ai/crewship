package composio

import (
	"context"
	"net/http"
	"net/http/httptest"
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
