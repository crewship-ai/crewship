package sidecar

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// newLegacyMemoryRouteServer builds a sidecar whose crew HAS per-agent tokens
// provisioned (alpha = boot agent, beta = sibling in the same container) and
// whose LEGACY HTTP memory surface (/memory/{read,write,search,status,reindex})
// is fully wired: engine + executor + scrubber, base path = alpha's tier.
//
// That base path is the whole point of the regression these tests cover: the
// legacy routes resolve the tier from s.agentMemoryBase — the BOOT agent's
// memory — with no identity resolution at all. A sibling (beta) that omits its
// Authorization header therefore reads and writes ALPHA's private tier.
func newLegacyMemoryRouteServer(t *testing.T, withTokens bool) (*Server, string) {
	t.Helper()
	base := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := memory.New(base, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	ex := newMemoryExecutor(silent)
	t.Cleanup(func() { ex.Close(time.Second) })

	s := &Server{
		memoryEngine:    eng,
		agentMemoryBase: base,
		memoryAgentSlug: "alpha",
		scrubber:        scrubber.New(),
		logger:          silent,
		memoryExec:      ex,
		crewMembers: []CrewMember{
			{ID: "agent-1", Slug: "alpha"},
			{ID: "agent-2", Slug: "beta"},
		},
		ipc: &IPCConfig{
			AgentID:     "agent-1",
			AgentSlug:   "alpha",
			CrewID:      "crew-1",
			WorkspaceID: "ws-1",
		},
	}
	if withTokens {
		s.ipc.AgentToken = "tok-alpha"
		s.crewMembers[0].AuthToken = "tok-alpha"
		s.crewMembers[1].AuthToken = "tok-beta"
	}
	return s, base
}

// loopbackRequest builds a request that satisfies the buildHandler
// isLocalhost(Host) && remoteIsLoopback(RemoteAddr) gate, so the sidecar's
// control-plane switch (rather than the forward proxy) handles it.
func loopbackRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:9119"
	r.RemoteAddr = "127.0.0.1:54321"
	return r
}

// TestLegacyMemoryRoutes_TokenlessRefused is the regression test for the
// residual left open by #1274 (CRE-153). #1274 added Server.tokenlessDowngrade
// and called it from handleMemoryMCPForAgent only. The five LEGACY memory
// routes registered ten lines away in buildHandler did ZERO identity
// resolution, so on a tokens-provisioned crew a sibling that simply omitted the
// Authorization header could still read the boot agent's private AGENT.md
// (200 + content) and overwrite it (201).
//
// Every route on the memory surface must refuse a token-less request with 403
// — the same status /query and /escalate already return — once per-agent
// tokens are in force.
func TestLegacyMemoryRoutes_TokenlessRefused(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)

	const secret = "alpha private secret\n"
	agentFile := filepath.Join(base, "AGENT.md")
	if err := os.WriteFile(agentFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"read", http.MethodGet, "/memory/read?file=AGENT.md", ""},
		{"write", http.MethodPost, "/memory/write", `{"file":"AGENT.md","content":"clobbered\n"}`},
		{"search", http.MethodPost, "/memory/search", `{"query":"secret"}`},
		{"status", http.MethodGet, "/memory/status", ""},
		{"reindex", http.MethodPost, "/memory/reindex", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := loopbackRequest(tc.method, tc.target, body)
			// Deliberately NO Authorization header — the downgrade attempt.
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "per-agent token required") {
				t.Errorf("body = %s, want it to mention 'per-agent token required'", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "alpha private secret") {
				t.Errorf("token-less request leaked the boot agent's memory: %s", w.Body.String())
			}
		})
	}

	got, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("read AGENT.md: %v", err)
	}
	if string(got) != secret {
		t.Fatalf("token-less write clobbered the boot agent's memory: %q", got)
	}
}

// TestLegacyMemoryRoutes_TokenedRequestStillWorks — the guard must only refuse
// the token-less case. A request carrying a recognized per-agent token keeps
// working exactly as before.
func TestLegacyMemoryRoutes_TokenedRequestStillWorks(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	if err := os.WriteFile(filepath.Join(base, "AGENT.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := loopbackRequest(http.MethodGet, "/memory/read?file=AGENT.md", nil)
	req.Header.Set("Authorization", "Bearer tok-alpha")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hello") {
		t.Errorf("body = %s, want the file content", w.Body.String())
	}
}

// TestLegacyMemoryRoutes_NoTokensProvisionedStillWorks — the refusal stays
// scoped to crews that HAVE tokens. A legacy (un-upgraded) deployment with no
// tokens anywhere keeps its prior behaviour, so upgrading the binary does not
// break crews whose agents don't carry tokens yet.
func TestLegacyMemoryRoutes_NoTokensProvisionedStillWorks(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, false)
	h := s.buildHandler(nil)
	if err := os.WriteFile(filepath.Join(base, "AGENT.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := loopbackRequest(http.MethodGet, "/memory/read?file=AGENT.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on a token-less deployment; body=%s", w.Code, w.Body.String())
	}
}

// TestMemoryMCPRoutes_TokenlessRefusedThroughRouter — the MCP memory paths go
// through the same chokepoint now, and the refusal is no longer a 200. It
// carries 403 (consistent with /query and /escalate, so a downgrade attempt
// stops looking like a success in access logs) and echoes the request id
// instead of the JSON-RPC null id.
func TestMemoryMCPRoutes_TokenlessRefusedThroughRouter(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)

	for _, target := range []string{"/mcp/memory", "/mcp/memory/beta"} {
		t.Run(target, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":42,"method":"tools/call",` +
				`"params":{"name":"memory.read","arguments":{"tier":"AGENT"}}}`
			req := loopbackRequest(http.MethodPost, target, strings.NewReader(body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body=%s", w.Code, w.Body.String())
			}
			var resp memoryMCPResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
			}
			if resp.Error == nil || !strings.Contains(resp.Error.Message, "per-agent token required") {
				t.Fatalf("want a 'per-agent token required' JSON-RPC error, got %s", w.Body.String())
			}
			if string(resp.ID) != "42" {
				t.Errorf("id = %s, want the request id 42 echoed back", resp.ID)
			}
		})
	}
}

// TestMCPRequestID covers the id-echo helper directly: JSON-RPC 2.0 allows
// string | number | null ids, and anything else (or an unparseable body) must
// degrade to null rather than reflecting attacker-shaped JSON.
func TestMCPRequestID(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"number", `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`, "7"},
		{"string", `{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`, `"abc"`},
		{"absent", `{"jsonrpc":"2.0","method":"tools/list"}`, "null"},
		{"explicit null", `{"jsonrpc":"2.0","id":null,"method":"tools/list"}`, "null"},
		{"object id rejected", `{"jsonrpc":"2.0","id":{"a":1},"method":"tools/list"}`, "null"},
		{"array id rejected", `{"jsonrpc":"2.0","id":[1],"method":"tools/list"}`, "null"},
		{"garbage", `not json at all`, "null"},
		{"empty", ``, "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp/memory", strings.NewReader(tc.body))
			if got := string(mcpRequestID(r)); got != tc.want {
				t.Errorf("mcpRequestID = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestMemoryMCP_ForgedTokenDirectCall_Is403 pins the status a forged token gets
// from handleMemoryMCPForAgent called DIRECTLY, bypassing the router.
//
// Honest about what it proves: the handler's own refuseUnauthorizedMemory call
// already answers 403 first, so this passed before the in-handler branch was
// changed from 200 to 403 too. It is a pin, not a regression reproduction —
// its job is to fail if a refactor moves or drops the chokepoint call and lets
// the stale 200 branch become live again, which would put a refusal back in
// the access log as a success.
func TestMemoryMCP_ForgedTokenDirectCall_Is403(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, true)

	req := loopbackRequest(http.MethodPost, "/mcp/memory/beta",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer tok-forged")
	w := httptest.NewRecorder()

	s.handleMemoryMCPForAgent(w, req, "beta")

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a forged token; body=%s", w.Code, w.Body.String())
	}
	var resp memoryMCPResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("error = %+v, want JSON-RPC -32001", resp.Error)
	}
}
