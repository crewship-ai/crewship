package orchestrator

// Coverage tests for exec_mcp.go: injectMCPOAuthTokens (token-file routing
// per server) and setupMCPConfig error/legacy paths. Reuses covContainer
// from exec_sidecar_cov_test.go.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// covTokenWriteRE extracts the base64 payload + destination from the
// injectMCPOAuthTokens write script:
// mkdir -p '<dir>' && printf '%s' '<b64>' | base64 -d > '<path>' && chmod 600 '<path>'
var covTokenWriteRE = regexp.MustCompile(`printf '%s' '([A-Za-z0-9+/=]+)' \| base64 -d > '([^']+)'`)

type covTokenWrite struct {
	path string
	body map[string]any
}

func covCollectTokenWrites(t *testing.T, c *covContainer) []covTokenWrite {
	t.Helper()
	var out []covTokenWrite
	for _, call := range c.snapshotCalls() {
		m := covTokenWriteRE.FindStringSubmatch(covScript(call))
		if len(m) != 3 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			t.Fatalf("decode token payload: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("unmarshal token payload: %v", err)
		}
		out = append(out, covTokenWrite{path: m[2], body: body})
	}
	return out
}

func TestInjectMCPOAuthTokens_NoOAuthCredsIsNoop(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	err := injectMCPOAuthTokens(context.Background(), c, "ctr1", "bob",
		[]MCPServerConfig{{Name: "gmail"}},
		[]Credential{{EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "x"}},
		covQuietLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.snapshotCalls()) != 0 {
		t.Errorf("no exec expected without OAuth tokens, got %d", len(c.snapshotCalls()))
	}
}

func TestInjectMCPOAuthTokens_MatchedServerWritesBothPaths(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	servers := []MCPServerConfig{
		{
			Name: "google-workspace",
			Args: []string{"-y", "@dguido/google-workspace-mcp"},
			Env:  map[string]string{"GOOGLE_CLIENT_ID": "${GOOGLE_CLIENT_ID}"},
		},
		{Name: ""}, // unnamed → skipped
	}
	creds := []Credential{
		// CLIENT_ID ref ties the server to credential cred-1.
		{ID: "cred-1", Type: "OAUTH2", EnvVarName: "GOOGLE_CLIENT_ID", PlainValue: "client-id"},
		// The access token row carries the actual token for cred-1.
		{ID: "tok-row", Type: "OAUTH2", EnvVarName: "_OAUTH_ACCESS_TOKEN:cred-1", PlainValue: "ya29.secret"},
	}
	if err := injectMCPOAuthTokens(context.Background(), c, "ctr1", "bob", servers, creds, covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	writes := covCollectTokenWrites(t, c)
	if len(writes) != 2 {
		t.Fatalf("expected 2 token writes (package dir + server dir), got %d: %+v", len(writes), writes)
	}
	wantPaths := map[string]bool{
		"/crew/agents/bob/.config/google-workspace-mcp/tokens.json": false,
		"/crew/agents/bob/.config/google-workspace/tokens.json":     false,
	}
	for _, w := range writes {
		if _, ok := wantPaths[w.path]; !ok {
			t.Errorf("unexpected token path %q", w.path)
			continue
		}
		wantPaths[w.path] = true
		if w.body["access_token"] != "ya29.secret" {
			t.Errorf("token body wrong at %s: %v", w.path, w.body)
		}
		if w.body["token_type"] != "Bearer" {
			t.Errorf("token_type wrong: %v", w.body)
		}
	}
	for p, seen := range wantPaths {
		if !seen {
			t.Errorf("missing token write to %s", p)
		}
	}
}

func TestInjectMCPOAuthTokens_SingleTokenFallback(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	// Server has no matching env ref, but exactly one OAuth token exists →
	// unambiguous fallback applies.
	servers := []MCPServerConfig{{Name: "notion"}}
	creds := []Credential{
		{ID: "t1", EnvVarName: "_OAUTH_ACCESS_TOKEN:credX", PlainValue: "tok-fallback"},
	}
	if err := injectMCPOAuthTokens(context.Background(), c, "ctr1", "bob", servers, creds, covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	writes := covCollectTokenWrites(t, c)
	if len(writes) != 1 {
		t.Fatalf("expected 1 token write, got %d", len(writes))
	}
	if writes[0].path != "/crew/agents/bob/.config/notion/tokens.json" {
		t.Errorf("wrong path: %q", writes[0].path)
	}
	if writes[0].body["access_token"] != "tok-fallback" {
		t.Errorf("wrong token: %v", writes[0].body)
	}
}

func TestInjectMCPOAuthTokens_AmbiguousTokensSkipUnmatchedServer(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	servers := []MCPServerConfig{{Name: "unmatched"}}
	creds := []Credential{
		{EnvVarName: "_OAUTH_ACCESS_TOKEN:a", PlainValue: "tok-a"},
		{EnvVarName: "_OAUTH_ACCESS_TOKEN:b", PlainValue: "tok-b"},
	}
	if err := injectMCPOAuthTokens(context.Background(), c, "ctr1", "bob", servers, creds, covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(covCollectTokenWrites(t, c)); got != 0 {
		t.Errorf("ambiguous tokens must not be injected, got %d writes", got)
	}
}

func TestInjectMCPOAuthTokens_ExecErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	c := &covContainer{route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
		return nil, errors.New("container gone")
	}}
	servers := []MCPServerConfig{{Name: "notion"}}
	creds := []Credential{{EnvVarName: "_OAUTH_ACCESS_TOKEN:x", PlainValue: "tok"}}
	if err := injectMCPOAuthTokens(context.Background(), c, "ctr1", "bob", servers, creds, covQuietLogger()); err != nil {
		t.Fatalf("write failures are best-effort, want nil error, got %v", err)
	}
}

// ---- setupMCPConfig ----

// covMCPWriteRE extracts the .mcp.json payload written by setupMCPConfig:
// echo '<b64>' | base64 -d > <home>/.mcp.json && chmod 600 ...
var covMCPWriteRE = regexp.MustCompile(`echo '([A-Za-z0-9+/=]+)' \| base64 -d > (\S+/\.mcp\.json)`)

func TestSetupMCPConfig_MergeError(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	err := setupMCPConfig(context.Background(), c, "ctr1", "bob",
		`{"mcpServers":{}}`, `{not json`, nil, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "merge MCP configs") {
		t.Fatalf("expected merge error, got %v", err)
	}
}

func TestSetupMCPConfig_LegacyServerList(t *testing.T) {
	t.Parallel()
	c := &covContainer{}
	servers := []MCPServerConfig{
		{Name: "files", Transport: "stdio", Command: "node", Args: []string{"server.js"}},
	}
	if err := setupMCPConfig(context.Background(), c, "ctr1", "bob", "", "", servers, covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	found := false
	for _, call := range c.snapshotCalls() {
		m := covMCPWriteRE.FindStringSubmatch(covScript(call))
		if len(m) != 3 {
			continue
		}
		found = true
		if m[2] != "/crew/agents/bob/.mcp.json" {
			t.Errorf("config written to wrong path: %q", m[2])
		}
		raw, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal .mcp.json: %v", err)
		}
	}
	if !found {
		t.Fatal("no .mcp.json write captured")
	}
	if _, ok := doc.MCPServers["files"]; !ok {
		t.Errorf("legacy server missing from config: %v", doc.MCPServers)
	}
	if _, ok := doc.MCPServers[MemoryMCPServerName]; !ok {
		t.Errorf("crewship-memory must be auto-injected: %v", doc.MCPServers)
	}
}

func TestSetupMCPConfig_WriteError(t *testing.T) {
	t.Parallel()
	c := &covContainer{route: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		if strings.Contains(covScript(cfg), ".mcp.json") {
			return nil, errors.New("disk full")
		}
		return nil, nil
	}}
	err := setupMCPConfig(context.Background(), c, "ctr1", "bob",
		`{"mcpServers":{"x":{"command":"node"}}}`, "", nil, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "write MCP config") {
		t.Fatalf("expected write error, got %v", err)
	}
}
