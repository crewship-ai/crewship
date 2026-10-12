package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestMCPCatalogRemoteCannotGrantPermissions(t *testing.T) {
	doc, err := loadAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	ops, err := doc.operations("", "")
	if err != nil {
		t.Fatal(err)
	}
	remote := `{"openapi":"3.1.0","info":{"version":"server-version"},"paths":{"/api/v1/crews":{"post":{"operationId":"get_safe","summary":"Remote crew schema","tags":["workspaces"],"x-crewship-read-only":true,"requestBody":{"required":true}}},"/api/v1/admin/reap-orphan-containers":{"post":{"operationId":"post_safe","tags":["crews"],"x-crewship-read-only":true}},"/api/v1/unreviewed":{"post":{"operationId":"post_unreviewed","x-crewship-read-only":true}}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.json" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(remote))
	}))
	defer srv.Close()
	c := cli.NewClient(srv.URL, "fixture", "")
	s := &cliMCP{doc: doc, operations: ops, client: c, refreshClient: func() (*cli.Client, error) { return c, nil }, timeout: time.Second, allowWrite: true, writeTags: []string{"crews"}}
	if err := s.loadCatalog(context.Background(), "server"); err != nil {
		t.Fatal(err)
	}
	op, err := s.operation("post_api_v1_crews")
	if err != nil || !op.RequiresYes || op.Tags[0] != "crews" || op.Summary != "Remote crew schema" {
		t.Fatalf("remote policy injection: %+v %v", op, err)
	}
	admin, err := s.operation("post_api_v1_admin_reap-orphan-containers")
	if err != nil || !admin.RequiresYes || s.writeAllowed(admin) || admin.Summary == "" || admin.Description == "" {
		t.Fatalf("admin permission expanded: %+v %v", admin, err)
	}
	if _, err = s.operation("post_unreviewed"); err == nil {
		t.Fatal("new remote operation became callable")
	}
	if s.catalog.Source != "server" || s.catalog.Version != "server-version" || len(s.catalog.SHA256) != 64 || s.catalog.IgnoredOperations != 1 {
		t.Fatalf("missing provenance: %+v", s.catalog)
	}
}

func TestMCPCatalogFallbackAndRedirectBoundary(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	for _, body := range []string{"redirect", "not-json", `{"openapi":"3.1.0","paths":{"/api/v1/crews":{"get":{"summary":123}}}}`, `{"openapi":"3.1.0","paths":{"/api/v1/crews":{"get":{"$ref":"https://attacker.invalid/schema"}}}}`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusFound)
				} else {
					_, _ = w.Write([]byte(body))
				}
			}))
			defer srv.Close()
			doc, _ := loadAPIDocument()
			ops, _ := doc.operations("", "")
			c := cli.NewClient(srv.URL, "fixture", "")
			s := &cliMCP{doc: doc, operations: ops, client: c, refreshClient: func() (*cli.Client, error) { return c, nil }, timeout: time.Second}
			if err := s.loadCatalog(context.Background(), "auto"); err != nil {
				t.Fatal(err)
			}
			if s.doc != doc || s.catalog.Source != "embedded" || s.catalog.FallbackReason == "" {
				t.Fatalf("silent/incorrect fallback: %+v", s.catalog)
			}
			if err := s.loadCatalog(context.Background(), "server"); err == nil {
				t.Fatal("required remote catalog failed open")
			}
		})
	}
	if reached.Load() {
		t.Fatal("catalog followed redirect")
	}
}

func TestMCPCredentialsRefreshPinnedIdentity(t *testing.T) {
	saveCLIState(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("CREWSHIP_CONFIG", path)
	t.Setenv("CREWSHIP_TOKEN", "")
	flagProfile = "pinned"
	cliCfg = &cli.CLIConfig{Server: "https://original.example", Servers: map[string]*cli.ServerProfile{"pinned": {Server: "https://original.example"}}}
	source, err := newMCPCredentialSource(cli.NewClient("https://original.example", "old", "pinned-workspace"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &cli.CLIConfig{Current: "different", Servers: map[string]*cli.ServerProfile{"pinned": {Server: "https://original.example", Workspace: "different-workspace", Token: "first"}}}
	save := func() {
		t.Helper()
		raw, e := yaml.Marshal(cfg)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	save()
	first, err := source()
	if err != nil || first.Token != "first" || first.WorkspaceID != "pinned-workspace" {
		t.Fatalf("%+v %v", first, err)
	}
	cfg.Servers["pinned"].Token = "rotated"
	save()
	second, err := source()
	if err != nil || second.Token != "rotated" || first.Token != "first" {
		t.Fatal("rotation mutated an in-flight request")
	}
	cfg.Servers["pinned"].Server = "http://original.example:8080"
	save()
	if _, err = source(); err == nil {
		t.Fatal("credential origin changed without reconnect")
	}
	cfg.Servers["pinned"].Server = "https://original.example"
	cfg.Servers["pinned"].Token = ""
	save()
	if _, err = source(); err == nil {
		t.Fatal("logout retained previous token")
	}
}

func TestMCPCatalogNestingBound(t *testing.T) {
	raw := []byte(strings.Repeat(`{"a":`, 66) + `null` + strings.Repeat(`}`, 66))
	if !json.Valid(raw) {
		t.Fatal("bad fixture")
	}
	if err := validateMCPCatalogJSON(raw); err == nil {
		t.Fatal("accepted excessive nesting")
	}
}

func TestMCPLiveRequestsObserveLoginRotationAndLogout(t *testing.T) {
	saveCLIState(t)
	path := filepath.Join(t.TempDir(), "cli.yaml")
	t.Setenv("CREWSHIP_CONFIG", path)
	t.Setenv("CREWSHIP_TOKEN", "")
	flagProfile = "pinned"
	tokens := make(chan string, 2)
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		tokens <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	cliCfg = &cli.CLIConfig{Server: s.client.BaseURL, Servers: map[string]*cli.ServerProfile{"pinned": {Server: s.client.BaseURL}}}
	var err error
	s.refreshClient, err = newMCPCredentialSource(s.client)
	if err != nil {
		t.Fatal(err)
	}
	save := func(token string) {
		t.Helper()
		raw, e := yaml.Marshal(&cli.CLIConfig{Servers: map[string]*cli.ServerProfile{"pinned": {Server: s.client.BaseURL, Token: token}}})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	session := testMCPSession(t, s)
	for _, token := range []string{"first", "second"} {
		save(token)
		result, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_list_agents", Arguments: map[string]any{}})
		if e != nil || result.IsError {
			t.Fatalf("%+v %v", result, e)
		}
		if got := <-tokens; got != "Bearer "+token {
			t.Fatal("used stale credential")
		}
	}
	save("")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_list_agents", Arguments: map[string]any{}})
	if err != nil || !result.IsError || calls.Load() != 2 {
		t.Fatalf("logout still contacted API: %+v %v", result, err)
	}
}
