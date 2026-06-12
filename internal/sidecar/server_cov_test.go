package sidecar

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func covLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestCovBuildHandlerRoutesAllControlPlanePaths drives every control-plane
// route through the full buildHandler dispatcher. The server has no IPC, no
// memory engines, and no MCP gateway, so each route resolves to a fast,
// deterministic handler response that proves the dispatcher matched the
// intended handler (503 "not configured" / 400 validation / 200 static)
// instead of falling through to the forward proxy.
func TestCovBuildHandlerRoutesAllControlPlanePaths(t *testing.T) {
	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	handler := srv.buildHandler(srv.proxy)

	tests := []struct {
		method   string
		path     string
		body     string
		wantCode int
	}{
		{"POST", "/memory/search", "", http.StatusBadRequest},
		{"POST", "/memory/write", "", http.StatusBadRequest},
		{"GET", "/memory/read", "", http.StatusBadRequest},
		{"GET", "/memory/status", "", http.StatusServiceUnavailable},
		{"POST", "/memory/reindex", "", http.StatusServiceUnavailable},
		{"POST", "/mcp/memory", "", http.StatusBadRequest},
		{"POST", "/assign", "", http.StatusServiceUnavailable},
		{"GET", "/results/abc123", "", http.StatusServiceUnavailable},
		{"POST", "/query", "", http.StatusServiceUnavailable},
		{"GET", "/standup", "", http.StatusServiceUnavailable},
		{"POST", "/escalate", "", http.StatusServiceUnavailable},
		{"POST", "/report-confidence", "", http.StatusServiceUnavailable},
		{"POST", "/mission/create", "", http.StatusServiceUnavailable},
		{"GET", "/mission/templates", "", http.StatusOK},
		{"GET", "/mission/m-1", "", http.StatusServiceUnavailable},
		{"POST", "/mission/m-1/start", "", http.StatusServiceUnavailable},
		{"POST", "/keeper/request", "", http.StatusServiceUnavailable},
		{"POST", "/keeper/execute", "", http.StatusServiceUnavailable},
		{"POST", "/expose-port", "", http.StatusServiceUnavailable},
		{"GET", "/crews", "", http.StatusServiceUnavailable},
		{"POST", "/crew/create", "", http.StatusServiceUnavailable},
		{"POST", "/agent/create", "", http.StatusServiceUnavailable},
		{"POST", "/spawn", "", http.StatusServiceUnavailable},
		{"GET", "/credentials", "", http.StatusServiceUnavailable},
		{"POST", "/agent-credentials", "", http.StatusServiceUnavailable},
		{"GET", "/crew-connections", "", http.StatusServiceUnavailable},
		{"POST", "/crew-connections", "", http.StatusServiceUnavailable},
		{"POST", "/issue/create", "", http.StatusServiceUnavailable},
		{"PATCH", "/manifest", "", http.StatusBadRequest},
		{"POST", "/pipelines/save", "", http.StatusServiceUnavailable},
		{"GET", "/pipelines", "", http.StatusServiceUnavailable},
		{"GET", "/pipelines/my-pipe", "", http.StatusServiceUnavailable},
		{"POST", "/pipelines/my-pipe/run", "", http.StatusServiceUnavailable},
		{"POST", "/pipelines/my-pipe/dry_run", "", http.StatusServiceUnavailable},
		{"POST", "/routines/schedules/create", "", http.StatusServiceUnavailable},
		{"POST", "/skills/generate", "", http.StatusServiceUnavailable},
		{"POST", "/credentials/create", "", http.StatusServiceUnavailable},
		{"POST", "/credentials/cred-1/rotate", "", http.StatusServiceUnavailable},
		{"GET", "/mcp/tools", "", http.StatusOK},
		{"POST", "/mcp/call", `{"server":"s","tool":"t"}`, http.StatusServiceUnavailable},
		{"GET", "/mcp/status", "", http.StatusOK},
		{"GET", "/connections", "", http.StatusServiceUnavailable},
		{"POST", "/connections/conn-1/message", "", http.StatusServiceUnavailable},
		{"GET", "/connections/conn-1/messages", "", http.StatusServiceUnavailable},
		{"GET", "/connections/conn-1/files", "", http.StatusServiceUnavailable},
		{"POST", "/connections/conn-1/files", "", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var body *strings.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tt.method, "http://localhost:9119"+tt.path, body)
			req.Host = "localhost:9119"
			req.RemoteAddr = "127.0.0.1:54321"
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("%s %s: expected %d, got %d: %s", tt.method, tt.path, tt.wantCode, w.Code, w.Body.String())
			}
		})
	}
}

// TestCovBuildHandlerGetManifestRoute is split from the route table because
// it depends on /crew/manifest.json not existing on the test host (the
// handler then serves the default manifest).
func TestCovBuildHandlerGetManifestRoute(t *testing.T) {
	if _, err := os.Stat(manifestPath); err == nil {
		t.Skipf("%s exists on this host; default-manifest assertion would be wrong", manifestPath)
	}

	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	handler := srv.buildHandler(srv.proxy)

	req := httptest.NewRequest("GET", "http://localhost:9119/manifest", nil)
	req.Host = "localhost:9119"
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var m CrewManifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid manifest JSON: %v", err)
	}
	if m.Version != 1 {
		t.Errorf("expected default manifest version 1, got %d", m.Version)
	}
}

// TestCovBuildHandlerNonLoopbackFallsThroughToProxy asserts the Patch-E
// gate: a request whose Host header claims localhost but whose TCP source
// is NOT loopback must bypass the control plane and hit the forward proxy
// (which answers 404 for unknown localhost paths via handleLocal).
func TestCovBuildHandlerNonLoopbackFallsThroughToProxy(t *testing.T) {
	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	handler := srv.buildHandler(srv.proxy)

	req := httptest.NewRequest("GET", "http://localhost:9119/standup", nil)
	req.Host = "localhost:9119"
	req.RemoteAddr = "192.0.2.1:1234" // httptest default: non-loopback
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Control plane would answer 503 (no IPC). The proxy's handleLocal
	// answers 404 for /standup — proof the gate routed to the proxy.
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 from proxy fallthrough, got %d: %s", w.Code, w.Body.String())
	}
}

// --- NewServer config branches ---

func TestCovNewServerUnknownNetworkModeFailsClosed(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:          "127.0.0.1:0",
		Logger:        covLogger(),
		NetworkPolicy: &NetworkPolicyConfig{Mode: "yolo"},
	})

	// Unknown mode must default to restricted → /health reports it.
	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()
	srv.proxy.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), `"network_mode":"restricted"`) {
		t.Errorf("expected restricted network mode, got %s", w.Body.String())
	}
}

func TestCovNewServerRestrictedModeAddsPolicyDomains(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: covLogger(),
		NetworkPolicy: &NetworkPolicyConfig{
			Mode:           "restricted",
			AllowedDomains: []string{"internal.example.com"},
		},
	})

	if !srv.Allowlist().IsAllowed("internal.example.com") {
		t.Error("policy-allowed domain should be in the allowlist")
	}
	if srv.Allowlist().IsAllowed("evil.example.org") {
		t.Error("unlisted domain must not be allowed in restricted mode")
	}

	req := httptest.NewRequest("GET", "http://localhost:9119/health", nil)
	req.Host = "localhost:9119"
	w := httptest.NewRecorder()
	srv.proxy.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"network_mode":"restricted"`) {
		t.Errorf("expected restricted network mode, got %s", w.Body.String())
	}
}

// TestCovNewServerMemoryInitFailureKeepsBasePaths verifies the documented
// degradation contract: when the FTS engine cannot initialize (base path is
// under a regular file), agentMemoryBase / crewMemoryBase are still set so
// path-based memory tools keep working, while the engines stay nil.
func TestCovNewServerMemoryInitFailureKeepsBasePaths(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badAgent := filepath.Join(blocker, "agent-mem")
	badCrew := filepath.Join(blocker, "crew-mem")

	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: covLogger(),
		Memory: &MemoryConfig{
			Enabled:        true,
			BasePath:       badAgent,
			AgentRole:      "lead",
			CrewMemoryPath: badCrew,
		},
	})

	if srv.agentMemoryBase != badAgent {
		t.Errorf("agentMemoryBase = %q, want %q", srv.agentMemoryBase, badAgent)
	}
	if srv.crewMemoryBase != badCrew {
		t.Errorf("crewMemoryBase = %q, want %q", srv.crewMemoryBase, badCrew)
	}
	if srv.memoryEngine != nil {
		t.Error("memoryEngine should be nil when FTS init fails")
	}
	if srv.crewMemoryEngine != nil {
		t.Error("crewMemoryEngine should be nil when FTS init fails")
	}
}

// --- proxyIPCJSON branches ---

func TestCovProxyIPCJSONInvalidUpstreamResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this is not json"))
	}))
	defer upstream.Close()

	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: covLogger(),
		IPC:    &IPCConfig{BaseURL: upstream.URL, Token: "tok"},
	})

	req := httptest.NewRequest("GET", "http://localhost:9119/x", nil)
	w := httptest.NewRecorder()
	srv.proxyIPCJSON(w, req, http.MethodGet, "/whatever", "test-label", nil)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid response from crewshipd") {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestCovProxyIPCJSONNoIPC(t *testing.T) {
	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger()})
	req := httptest.NewRequest("GET", "http://localhost:9119/x", nil)
	w := httptest.NewRecorder()
	srv.proxyIPCJSON(w, req, http.MethodGet, "/whatever", "label", nil)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "IPC not configured") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovProxyIPCJSONSendsBodyAndToken(t *testing.T) {
	var gotToken, gotCT, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Internal-Token")
		gotCT = r.Header.Get("Content-Type")
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		writeJSONResponse(w, http.StatusCreated, map[string]string{"id": "new-1"})
	}))
	defer upstream.Close()

	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: covLogger(),
		IPC:    &IPCConfig{BaseURL: upstream.URL, Token: "secret-token"},
	})

	req := httptest.NewRequest("POST", "http://localhost:9119/x", nil)
	w := httptest.NewRecorder()
	srv.proxyIPCJSON(w, req, http.MethodPost, "/api/v1/internal/things", "thing", []byte(`{"a":1}`))

	if w.Code != http.StatusCreated {
		t.Fatalf("expected upstream 201 passthrough, got %d", w.Code)
	}
	if gotToken != "secret-token" {
		t.Errorf("X-Internal-Token = %q", gotToken)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if gotBody != `{"a":1}` {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(w.Body.String(), `"id":"new-1"`) {
		t.Errorf("response body = %s", w.Body.String())
	}
}

// --- Start lifecycle branches ---

func TestCovStartListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := NewServer(ServerConfig{Addr: ln.Addr().String(), Logger: covLogger()})
	err = srv.Start(context.Background())
	if err == nil {
		t.Fatal("expected listen error on occupied port")
	}
	if !strings.Contains(err.Error(), "sidecar listen") {
		t.Errorf("error = %v, want sidecar listen wrap", err)
	}
}

// TestCovStartShutdownWithCrewMemoryAndMCP covers the Start branches that
// only fire when a crew memory engine and an MCP gateway are configured:
// the periodic-reindex goroutine, the background MCP connect, and the
// teardown of both on context cancellation.
func TestCovStartShutdownWithCrewMemoryAndMCP(t *testing.T) {
	agentDir := t.TempDir()
	crewDir := t.TempDir()
	os.WriteFile(filepath.Join(crewDir, "CREW.md"), []byte("# Crew\nShared fact."), 0o644)

	mcp := mockMCPServer(t, []mcpToolDef{{Name: "echo", Description: "echo tool"}})
	defer mcp.Close()

	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: covLogger(),
		Memory: &MemoryConfig{
			Enabled:        true,
			BasePath:       agentDir,
			AgentRole:      "lead",
			CrewMemoryPath: crewDir,
		},
		MCPServers: []MCPServerInput{{
			ID: "srv-1", Name: "test-mcp", DisplayName: "Test MCP",
			Transport: "streamable-http", Endpoint: mcp.URL,
		}},
	})
	if srv.crewMemoryEngine == nil {
		t.Fatal("crew memory engine should be initialized")
	}
	if srv.mcpGateway == nil {
		t.Fatal("mcp gateway should be initialized")
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("server never became ready")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("clean shutdown should return nil, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

// TestCovStartServeErrorBranch covers the errCh select arm: closing the
// http.Server out from under Start makes Serve return ErrServerClosed,
// which closes errCh without a value — Start must run its cleanup and
// return nil.
func TestCovStartServeErrorBranch(t *testing.T) {
	crewDir := t.TempDir()
	srv := NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		Logger: covLogger(),
		Memory: &MemoryConfig{
			Enabled:        true,
			BasePath:       t.TempDir(),
			AgentRole:      "lead",
			CrewMemoryPath: crewDir,
		},
		MCPServers: []MCPServerInput{{
			ID: "srv-1", Name: "never-up", DisplayName: "Never Up",
			Transport: "streamable-http", Endpoint: "http://127.0.0.1:1/mcp",
		}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("server never became ready")
	}

	srv.httpServer.Close()
	// Cancel ctx too so the crew reindex goroutine exits and Start can
	// finish its cleanup (it waits on crewReindexDone).
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil from closed-server branch, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after server close")
	}
}
