package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// FIX B — loadAgentData accepts an OPTIONAL ?workspace_id= scope. When present
// and the agent belongs to a different workspace the row must not match (404,
// not leaked across tenants). When matching, the agent resolves normally.
//
// Prefix: TestSecResolveAgentScope*.
// ---------------------------------------------------------------------------

// TestSecResolveAgentScopeMismatch404 seeds an agent in WS-B and resolves it
// with ?workspace_id=WS-A. Pre-fix loadAgentData was `WHERE a.id = ?` with no
// scope, so the cross-tenant resolve returned 200 + the agent config. Post-fix
// the workspace predicate yields no row → 404.
func TestSecResolveAgentScopeMismatch404(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)

	wsA := "scope-ws-a"
	wsB := "scope-ws-b"
	for _, ws := range []string{wsA, wsB} {
		if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, ws, ws, ws); err != nil {
			t.Fatalf("insert workspace %s: %v", ws, err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		 VALUES ('scoped-agent', ?, 'Scoped', 'scoped', 'IDLE', 'CLAUDE_CODE', 'default', 600, 0)`, wsB); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())

	// Caller scoped to WS-A reaches for WS-B's agent.
	req := httptest.NewRequest("GET",
		"/api/v1/internal/agents/scoped-agent/resolve?workspace_id="+wsA, nil)
	req.SetPathValue("agentId", "scoped-agent")
	rr := httptest.NewRecorder()
	h.ResolveAgent(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace resolve: status = %d, want 404, body: %s", rr.Code, rr.Body.String())
	}
}

// TestSecResolveAgentScopeMatchOK is the positive companion: a caller scoped to
// the agent's own workspace still resolves it. Guards against the scope
// predicate breaking the legitimate same-tenant path.
func TestSecResolveAgentScopeMatchOK(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)

	wsB := "scope-ws-b"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		 VALUES ('scoped-agent', ?, 'Scoped', 'scoped', 'IDLE', 'CLAUDE_CODE', 'default', 600, 0)`, wsB); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())

	req := httptest.NewRequest("GET",
		"/api/v1/internal/agents/scoped-agent/resolve?workspace_id="+wsB, nil)
	req.SetPathValue("agentId", "scoped-agent")
	rr := httptest.NewRecorder()
	h.ResolveAgent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("same-workspace resolve: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
}

// TestSecResolveAgentNoScopeStillResolves confirms the param is OPTIONAL: a
// resolve with NO workspace_id keeps the legacy id-only behavior (chat resolve
// and the webhook resolve path rely on this).
func TestSecResolveAgentNoScopeStillResolves(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)

	wsB := "scope-ws-b"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		 VALUES ('scoped-agent', ?, 'Scoped', 'scoped', 'IDLE', 'CLAUDE_CODE', 'default', 600, 0)`, wsB); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "tok", covICILogger())

	req := httptest.NewRequest("GET", "/api/v1/internal/agents/scoped-agent/resolve", nil)
	req.SetPathValue("agentId", "scoped-agent")
	rr := httptest.NewRecorder()
	h.ResolveAgent(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("no-scope resolve: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
}
