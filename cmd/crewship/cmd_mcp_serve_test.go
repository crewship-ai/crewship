package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/crewship-ai/crewship/internal/cli"
)

func testCLIMCP(t *testing.T, handler http.HandlerFunc) (*cliMCP, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler(w, r) }))
	t.Cleanup(server.Close)
	doc, err := loadAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	ops, err := doc.operations("", "")
	if err != nil {
		t.Fatal(err)
	}
	client := cli.NewClient(server.URL, "test-token", "cworkspace012345678901234")
	return &cliMCP{doc: doc, operations: ops, client: client, authenticate: func() error { return nil }, timeout: time.Second, responseLimit: 1024}, &calls
}

func mcpOpID(t *testing.T, s *cliMCP, method, path string) string {
	t.Helper()
	for _, op := range s.operations {
		if op.Method == method && op.Path == path {
			return op.ID
		}
	}
	t.Fatalf("missing %s %s", method, path)
	return ""
}

func testMCPSession(t *testing.T, s *cliMCP, options ...*mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	a, b := mcp.NewInMemoryTransports()
	server, err := s.server()
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := server.Connect(context.Background(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	var opts *mcp.ClientOptions
	if len(options) > 0 {
		opts = options[0]
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, opts)
	session, err := client.Connect(context.Background(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestCLIMCPWireDiscoveryAndJSONBody(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Query().Get("workspace_id") != "cworkspace012345678901234" {
			t.Errorf("identity lost")
		}
		if r.Method != "POST" || r.URL.Path != "/api/v1/agents" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body["number"]) != "9007199254740993" {
			t.Errorf("body number changed: %v %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "test-etag")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":9007199254740993}`)
	})
	s.allowWrite = true
	session := testMCPSession(t, s)
	ctx := context.Background()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 13 {
		t.Fatalf("expected bounded 13-tool surface, got %d", len(tools.Tools))
	}
	raw, _ := json.Marshal(tools)
	t.Logf("MCP manifest: %d tools, %d JSON bytes", len(tools.Tools), len(raw))
	if len(raw) > 12000 {
		t.Fatalf("tool manifest too large: %d", len(raw))
	}
	for _, name := range []string{"crewship_guide", "crewship_search"} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil || result.IsError || result.StructuredContent == nil {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
	}
	id := mcpOpID(t, s, "POST", "/api/v1/agents")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "crewship_schema", Arguments: map[string]any{"operation_id": id}})
	if err != nil || result.IsError {
		t.Fatalf("schema: %+v %v", result, err)
	}
	if calls.Load() != 0 {
		t.Fatal("discovery made network requests")
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "crewship_write", Arguments: json.RawMessage(fmt.Sprintf(`{"operation_id":%q,"confirm_write":true,"body":{"number":9007199254740993}}`, id))})
	if err != nil || result.IsError {
		t.Fatalf("request: %+v %v content=%+v", result, err, result.Content[0].(*mcp.TextContent).Text)
	}
	// Inspect text, since clients are allowed to decode structuredContent into float64.
	content := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(content, "9007199254740993") || !strings.Contains(content, "201") || !strings.Contains(content, "test-etag") {
		t.Fatal(content)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCLIMCPWritePolicyAndDryRun(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) })
	id := mcpOpID(t, s, "POST", "/api/v1/agents")
	for _, tc := range []struct {
		allow, confirm, dry bool
		wantErr             bool
	}{{false, false, false, true}, {false, true, false, true}, {true, false, false, true}, {false, false, true, false}, {true, true, false, false}} {
		s.allowWrite = tc.allow
		before := calls.Load()
		_, err := s.request(context.Background(), mcpRequestInput{OperationID: id, ConfirmWrite: tc.confirm, DryRun: tc.dry, Body: json.RawMessage(`{}`)})
		if (err != nil) != tc.wantErr {
			t.Fatalf("%+v: %v", tc, err)
		}
		if (tc.wantErr || tc.dry) && calls.Load() != before {
			t.Fatal("denied request or dry-run made network call")
		}
	}
}

func TestCLIMCPPathParameters(t *testing.T) {
	for _, value := range []string{"", ".", "..", "a/b", "a\\b", "%2e%2e", "%252f", "a\x00", "{id}"} {
		if _, err := mcpOperationPath("/api/v1/agents/{agentId}", map[string]string{"agentId": value}); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	for _, params := range []map[string]string{nil, {"agentId": "ok", "extra": "oops"}} {
		if _, err := mcpOperationPath("/api/v1/agents/{agentId}", params); err == nil {
			t.Errorf("accepted %v", params)
		}
	}
	path, err := mcpOperationPath("/api/v1/agents/{agentId}", map[string]string{"agentId": "a?b#c d"})
	if err != nil || path != "/api/v1/agents/a%3Fb%23c%20d" {
		t.Fatalf("%s %v", path, err)
	}
}

func TestCLIMCPGuardsAndErrors(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("case") == "large" {
			fmt.Fprint(w, strings.Repeat(" ", 2048))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"denied","missing_integrations":["github"]}`)
	})
	id := mcpOpID(t, s, "GET", "/api/v1/agents")
	for _, in := range []mcpRequestInput{
		{OperationID: "https://example.com/"},
		{OperationID: id, Headers: []string{"Authorization=bad"}},
		{OperationID: id, Headers: []string{"X-Workspace-ID=bad"}},
		{OperationID: id, Body: json.RawMessage(`{}`)},
		{OperationID: id, Query: []string{"missing-equals"}},
		{OperationID: id, Query: []string{"workspace_id=other"}},
	} {
		if _, err := s.request(context.Background(), in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid inputs reached server")
	}
	session := testMCPSession(t, s)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_read", Arguments: map[string]any{"operation_id": id}})
	if err != nil || !result.IsError {
		t.Fatalf("%+v %v", result, err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"exit_code":4`) || !strings.Contains(text, `"status":403`) || !strings.Contains(text, `"missing_integrations":["github"]`) {
		t.Fatalf("missing typed error: %+v", result)
	}
	if _, err := s.request(context.Background(), mcpRequestInput{OperationID: id, Query: []string{"case=large"}}); err == nil || !strings.Contains(err.Error(), "max-response-bytes") {
		t.Fatalf("size limit: %v", err)
	}
}

func TestCLIMCPConcurrentRequests(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"n":%q}`, r.URL.Query().Get("n")) })
	id := mcpOpID(t, s, "GET", "/api/v1/agents")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := fmt.Sprint(i)
			result, err := s.request(context.Background(), mcpRequestInput{OperationID: id, Query: []string{"n=" + n}})
			if err != nil {
				t.Error(err)
				return
			}
			raw, _ := json.Marshal(result)
			if !strings.Contains(string(raw), `"n":"`+n+`"`) {
				t.Errorf("mixed result: %s", raw)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 12 {
		t.Fatal(calls.Load())
	}
}

func TestCLIMCPSearchPagination(t *testing.T) {
	s, _ := testCLIMCP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP call") })
	for _, in := range []mcpSearchInput{{Limit: 101}, {Offset: -1}, {Method: "TRACE"}} {
		if _, err := s.search(in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	first, err := s.search(mcpSearchInput{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	page := first.(map[string]any)
	if len(page["operations"].([]apiOperation)) != 1 || page["next_offset"] != 1 {
		t.Fatal(page)
	}
	last, err := s.search(mcpSearchInput{Offset: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if last.(map[string]any)["next_offset"] != nil {
		t.Fatal(last)
	}
}

func TestCLIMCPWireRejectsIdentityAndFileArguments(t *testing.T) {
	s, calls := testCLIMCP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected network call") })
	session := testMCPSession(t, s)
	id := mcpOpID(t, s, "GET", "/api/v1/agents")
	for _, extra := range []string{`"server":"https://example.com"`, `"input":"/etc/passwd"`, `"output":"/tmp/file"`, `"confirm_write":"true"`, `"path_params":{"id":3}`, `"query":"workspace_id=other"`} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_read", Arguments: json.RawMessage(fmt.Sprintf(`{"operation_id":%q,%s}`, id, extra))})
		if err != nil || !result.IsError {
			t.Fatalf("accepted %s: %+v %v", extra, result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal(calls.Load())
	}
}

func TestCLIMCPRedirectAndCancellation(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("wait") == "true" {
			<-r.Context().Done()
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	id := mcpOpID(t, s, "GET", "/api/v1/agents")
	if _, err := s.request(context.Background(), mcpRequestInput{OperationID: id}); err == nil {
		t.Fatal("redirect accepted")
	}
	if redirected.Load() != 0 || calls.Load() != 1 {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.request(ctx, mcpRequestInput{OperationID: id}); err == nil {
		t.Fatal("cancellation ignored")
	}
	s.timeout = 20 * time.Millisecond
	if _, err := s.request(context.Background(), mcpRequestInput{OperationID: id, Query: []string{"wait=true"}}); err == nil {
		t.Fatal("timeout ignored")
	}
}

// Exercise the actual entrypoint: configuration loading, protocol-only stdout,
// startup policy and embedded assets must survive distribution as one binary.
func TestCLIMCPBinaryStdio(t *testing.T) {
	bin := buildCrewshipBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, bin, "mcp", "serve")
	child.Env = append(os.Environ(), "HOME="+t.TempDir())
	var stderr bytes.Buffer
	child.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "binary-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: child}, nil)
	if err != nil {
		t.Fatalf("stdio connect: %v; stderr=%s", err, stderr.String())
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 13 {
		t.Fatalf("tools: %+v %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "crewship_guide", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("guide: %+v %v", result, err)
	}
	if !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "name: crewship") {
		t.Fatal("embedded skill missing")
	}
}
