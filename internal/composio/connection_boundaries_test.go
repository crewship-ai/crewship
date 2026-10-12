package composio

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestConnectionDiscoveryAndScopedTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("discovery mutated remote: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/auth_configs":
			_, _ = w.Write([]byte(`{"items":[{"id":"ac-mail","toolkit":{"slug":"gmail"}},{"id":"ac-git","toolkit":{"slug":"github"}}]}`))
		case "/api/v3.1/trigger_instances/active":
			_, _ = w.Write([]byte(`{"items":[{"id":"trigger-1","user_id":"agent-1","trigger_name":"NEW_MESSAGE"}]}`))
		default:
			t.Errorf("unexpected path: %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := NewClient("test-value", srv.URL)
	for _, tc := range []struct{ slug, id string }{{"github", "ac-git"}, {"absent", ""}} {
		id, err := c.FindAuthConfig(t.Context(), tc.slug)
		if err != nil || id != tc.id {
			t.Fatalf("lookup=%q %v", id, err)
		}
	}
	triggers, err := c.ListActiveTriggers(t.Context())
	if err != nil || len(triggers) != 1 || triggers[0].ID != "trigger-1" || triggers[0].UserID != "agent-1" {
		t.Fatalf("triggers=%+v %v", triggers, err)
	}
	u, err := url.Parse(c.MCPUserURL("server/a?b", "user&admin=true"))
	if err != nil {
		t.Fatal(err)
	}
	if u.EscapedPath() != "/v3/mcp/server%2Fa%3Fb/mcp" || u.Query().Get("user_id") != "user&admin=true" || len(u.Query()) != 1 {
		t.Fatalf("scope escaped URL component: %s", u)
	}
}

func TestManagedAuthCreationSupportsBothResponseShapes(t *testing.T) {
	for _, body := range []string{`{"id":"flat"}`, `{"id":"flat","auth_config":{"id":"nested"}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v3.1/auth_configs" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				expected := map[string]any{"toolkit": map[string]any{"slug": "github"}, "auth_config": map[string]any{"type": "use_composio_managed_auth", "name": "Review app"}}
				if !reflect.DeepEqual(request, expected) {
					t.Errorf("auth creation=%+v", request)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()
			id, err := NewClient("test-value", srv.URL).CreateManagedAuthConfig(t.Context(), "github", "Review app")
			want := "flat"
			if body != `{"id":"flat"}` {
				want = "nested"
			}
			if err != nil || id != want {
				t.Fatalf("auth config=%q %v", id, err)
			}
		})
	}
}

func TestConnectLinkKeepsUserScopeAndOptionalCallback(t *testing.T) {
	for _, callback := range []string{"", "https://crewship.example/connected?state=one"} {
		t.Run(callback, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v3.1/connected_accounts/link" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["auth_config_id"] != "auth" || body["user_id"] != "agent" || body["callback_url"] != callback {
					t.Errorf("link scope=%v", body)
				}
				if _, present := body["callback_url"]; present != (callback != "") {
					t.Error("empty callback must be omitted")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"link_token":"fixture","redirect_url":"https://connect.example/session","expires_at":"tomorrow","connected_account_id":"account"}`))
			}))
			defer srv.Close()
			link, err := NewClient("test-value", srv.URL).CreateConnectLink(t.Context(), "auth", "agent", callback)
			if err != nil || link.ConnectedAccountID != "account" || link.LinkToken != "fixture" || link.RedirectURL != "https://connect.example/session" || link.ExpiresAt != "tomorrow" {
				t.Fatalf("link=%+v %v", link, err)
			}
		})
	}
}

func TestMCPCreateRetriesOnlyByNarrowingRequestedTools(t *testing.T) {
	requested := []string{"OLD_TOOL", "SAFE_TOOL"}
	var attempts [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AllowedTools []string `json:"allowed_tools"`
			Managed      bool     `json:"managed_auth_via_composio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !body.Managed {
			t.Error("managed authentication disabled")
		}
		attempts = append(attempts, body.AllowedTools)
		w.Header().Set("Content-Type", "application/json")
		if len(attempts) == 1 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"MCP_InvalidToolsProvided: OLD_TOOL"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"server","mcp_url":"https://mcp.example/scoped"}`))
	}))
	defer srv.Close()
	id, endpoint, err := NewClient("test-value", srv.URL).CreateMCPServer(t.Context(), "agent scope", []string{"auth"}, requested)
	if err != nil || id != "server" || endpoint != "https://mcp.example/scoped" {
		t.Fatalf("creation=%s %s %v", id, endpoint, err)
	}
	if !reflect.DeepEqual(attempts, [][]string{{"OLD_TOOL", "SAFE_TOOL"}, {"SAFE_TOOL"}}) || !reflect.DeepEqual(requested, []string{"OLD_TOOL", "SAFE_TOOL"}) {
		t.Fatalf("scope changed unexpectedly: attempts=%v input=%v", attempts, requested)
	}
}

func TestMCPCreateRefusesEmptyWidenedScopeAndBoundsRetries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tools   []string
		message string
		calls   int
	}{
		{"full mode", nil, "MCP_InvalidToolsProvided: OLD", 1},
		{"all invalid", []string{"OLD"}, "MCP_InvalidToolsProvided: OLD", 1},
		{"unrecognized tool", []string{"SAFE"}, "MCP_InvalidToolsProvided: OTHER", 1},
		{"non-tool error", []string{"SAFE"}, "permission denied", 1},
		{"retry limit", []string{"TOOL_A", "TOOL_B", "TOOL_C", "TOOL_D", "TOOL_E"}, "", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Tools []string `json:"allowed_tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(tc.tools) > 0 && len(body.Tools) == 0 {
					t.Error("restricted scope widened to all tools")
				}
				calls++
				msg := tc.message
				if msg == "" {
					msg = fmt.Sprintf("MCP_InvalidToolsProvided: %s", body.Tools[0])
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
			}))
			defer srv.Close()
			id, endpoint, err := NewClient("test-value", srv.URL).CreateMCPServer(t.Context(), "agent scope", []string{"auth"}, tc.tools)
			if err == nil || id != "" || endpoint != "" || calls != tc.calls {
				t.Fatalf("refusal: id=%s endpoint=%s calls=%d error=%v", id, endpoint, calls, err)
			}
		})
	}
}

func TestConnectionDiscoveryAndProvisioningPropagateUpstreamRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer srv.Close()
	c := NewClient("test-value", srv.URL)
	if _, err := c.ListActiveTriggers(t.Context()); err == nil {
		t.Fatal("trigger failure became empty inventory")
	}
	if _, err := c.FindAuthConfig(t.Context(), "gmail"); err == nil {
		t.Fatal("auth lookup failure became not found")
	}
	if _, err := c.CreateManagedAuthConfig(t.Context(), "gmail", "mail"); err == nil {
		t.Fatal("auth creation failure became success")
	}
	if _, err := c.CreateConnectLink(t.Context(), "auth", "agent", ""); err == nil {
		t.Fatal("connect failure became empty link")
	}
}
