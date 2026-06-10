package api

// PR-F24 hardening — enforcement-coverage closure for the
// workspace-bound X-Internal-Token (findings F-1…F-5).
//
// The crypto/issuance landed earlier; what these tests pin is that a
// bound ws-A token can no longer READ or WRITE ws-B rows through any
// internal handler. The exploit shape is always the same: seed two
// workspaces, drive a handler with a ws-A-bound token (via the real
// requireInternal chain so the bound workspace is injected the way
// production does it), and assert the ws-B row is unreachable (404 /
// empty / 403) — never returned or mutated.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

const scopeMaster = "scope-master-secret-0123456789abcdef"

type scopeIDs struct {
	wsA, crewA, agentA, chatA string
	wsB, crewB, agentB, chatB string
}

func seedScope(t *testing.T) (*InternalHandler, scopeIDs) {
	t.Helper()
	db := setupTestDB(t)
	h := NewInternalHandler(db, scopeMaster, testLogger())

	var ids scopeIDs
	ids.wsA, ids.crewA, ids.agentA, ids.chatA = "ws_a", "crew_a", "agent_a", "chat_a"
	ids.wsB, ids.crewB, ids.agentB, ids.chatB = "ws_b", "crew_b", "agent_b", "chat_b"

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed exec failed: %v\nquery: %s", err, q)
		}
	}
	ownerID := seedTestUser(t, db)
	for _, w := range []struct{ id, slug string }{{ids.wsA, "wsa"}, {ids.wsB, "wsb"}} {
		exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, w.id, w.id, w.slug)
	}
	for _, c := range []struct{ id, ws, slug string }{
		{ids.crewA, ids.wsA, "crewa"}, {ids.crewB, ids.wsB, "crewb"},
	} {
		exec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, ?, ?, 'PRE')`,
			c.id, c.ws, c.id, c.slug)
	}
	for _, a := range []struct{ id, ws, crew, slug, secret string }{
		{ids.agentA, ids.wsA, ids.crewA, "aagent", "secret-a"},
		{ids.agentB, ids.wsB, ids.crewB, "bagent", "secret-b-stolen"},
	} {
		exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled, webhook_secret)
		      VALUES (?, ?, ?, ?, ?, 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0, ?)`,
			a.id, a.ws, a.crew, a.id, a.slug, a.secret)
	}
	for _, c := range []struct{ id, ws, agent string }{
		{ids.chatA, ids.wsA, ids.agentA}, {ids.chatB, ids.wsB, ids.agentB},
	} {
		exec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status, started_at, created_at, title)
		      VALUES (?, ?, ?, 'CHAT', 'ACTIVE', datetime('now'), datetime('now'), '')`,
			c.id, c.agent, c.ws)
	}
	// A credential in each workspace so ListCredentials cross-tenant leak
	// is observable.
	for _, c := range []struct{ id, ws, name string }{
		{"cred_a", ids.wsA, "cred-a"}, {"cred_b", ids.wsB, "cred-b"},
	} {
		exec(`INSERT INTO credentials (id, workspace_id, name, type, provider, encrypted_value, status, security_level, created_by, created_at)
		      VALUES (?, ?, ?, 'API_KEY', 'anthropic', 'enc', 'ACTIVE', 1, ?, datetime('now'))`,
			c.id, c.ws, c.name, ownerID)
	}
	return h, ids
}

// boundReq builds a request authenticated with a ws-A-bound token from a
// loopback origin (so the network gate + master-loopback pin are both
// satisfied) and routed through the real requireInternal chain so the
// bound workspace is injected exactly as production does.
func boundReq(method, target string, body []byte, master, ws string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Internal-Token", internaltoken.DeriveWorkspaceToken(master, ws))
	return r
}

func setPathValue(r *http.Request, key, val string) *http.Request {
	r.SetPathValue(key, val)
	return r
}

// ---------------------------------------------------------------------------
// F-1 CRITICAL — GetWebhookSecret cross-tenant secret theft.
// ws-A token, no workspace query, asking for ws-B's agent secret → 404.
// ---------------------------------------------------------------------------
func TestSecBinding_F1_WebhookSecretCrossTenant(t *testing.T) {
	h, ids := seedScope(t)
	rr := httptest.NewRecorder()
	req := setPathValue(
		boundReq(http.MethodGet, "/x", nil, scopeMaster, ids.wsA),
		"agentId", ids.agentB)
	h.requireInternal(http.HandlerFunc(h.GetWebhookSecret)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (ws-A token must not read ws-B webhook secret); body=%s",
			rr.Code, rr.Body.String())
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("secret-b-stolen")) {
		t.Fatalf("ws-B webhook secret leaked across tenant boundary: %s", rr.Body.String())
	}
}

// Own-workspace still works.
func TestSecBinding_F1_WebhookSecretOwnTenant(t *testing.T) {
	h, ids := seedScope(t)
	rr := httptest.NewRecorder()
	req := setPathValue(
		boundReq(http.MethodGet, "/x", nil, scopeMaster, ids.wsA),
		"agentId", ids.agentA)
	h.requireInternal(http.HandlerFunc(h.GetWebhookSecret)).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for own-workspace secret; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("secret-a")) {
		t.Fatalf("own webhook secret not returned: %s", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// F-2 HIGH — ResolveChat / ResolveAgent cross-tenant config read.
// ws-A token resolving ws-B's chat must 404.
// ---------------------------------------------------------------------------
func TestSecBinding_F2_ResolveChatCrossTenant(t *testing.T) {
	h, ids := seedScope(t)
	rr := httptest.NewRecorder()
	req := setPathValue(
		boundReq(http.MethodGet, "/x", nil, scopeMaster, ids.wsA),
		"chatId", ids.chatB)
	h.requireInternal(http.HandlerFunc(h.ResolveChat)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (ws-A token must not resolve ws-B chat); body=%s",
			rr.Code, rr.Body.String())
	}
}

func TestSecBinding_F2_ResolveAgentCrossTenant(t *testing.T) {
	h, ids := seedScope(t)
	rr := httptest.NewRecorder()
	req := setPathValue(
		boundReq(http.MethodGet, "/x", nil, scopeMaster, ids.wsA),
		"agentId", ids.agentB)
	h.requireInternal(http.HandlerFunc(h.ResolveAgent)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (ws-A token must not resolve ws-B agent config); body=%s",
			rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// F-3 HIGH — ListCredentials cross-tenant metadata leak.
// ws-A token (no workspace query) must see ONLY ws-A credentials.
// ---------------------------------------------------------------------------
func TestSecBinding_F3_ListCredentialsScoped(t *testing.T) {
	h, ids := seedScope(t)
	rr := httptest.NewRecorder()
	req := boundReq(http.MethodGet, "/x", nil, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(h.ListCredentials)).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var creds []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &creds); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	// Assert both directions: ws-A's own cred is present (so a regression
	// that drops the bound workspace's own rows fails here, not just an
	// empty list), and ws-B's cred is absent.
	var sawCredA, sawCredB bool
	for _, c := range creds {
		if c["workspace_id"] != ids.wsA {
			t.Fatalf("ListCredentials leaked a non-ws_a credential: %+v", c)
		}
		if c["id"] == "cred_a" {
			sawCredA = true
		}
		if c["id"] == "cred_b" {
			sawCredB = true
		}
	}
	if !sawCredA {
		t.Fatalf("ListCredentials dropped ws-A's own credential cred_a; got %d creds: %+v", len(creds), creds)
	}
	if sawCredB {
		t.Fatalf("ListCredentials leaked ws-B's credential cred_b across the tenant boundary")
	}
}

// ---------------------------------------------------------------------------
// F-4 HIGH — body-workspace writers must reject a foreign body workspace.
// ws-A token POSTing an issue into ws-B must 403.
// ---------------------------------------------------------------------------
// countRows returns COUNT(*) over the given table, failing the test on a
// query error. Used to pin "the 403 wrote nothing" — a handler that
// writes before returning 403 would otherwise still pass on status alone.
func scopeCountRows(t *testing.T, h *InternalHandler, table string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestSecBinding_F4_IssueCreateForeignBody403(t *testing.T) {
	h, ids := seedScope(t)
	ih := NewInternalIssueHandler(h.db, nil, testLogger())
	before := scopeCountRows(t, h, "missions")
	body, _ := json.Marshal(map[string]string{
		"workspace_id": ids.wsB, "crew_id": ids.crewB, "title": "x",
	})
	rr := httptest.NewRecorder()
	req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(ih.Create)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ws-A token must not create issue in ws-B); body=%s",
			rr.Code, rr.Body.String())
	}
	if after := scopeCountRows(t, h, "missions"); after != before {
		t.Fatalf("issue (missions) row written despite 403: count %d→%d", before, after)
	}
}

func TestSecBinding_F4_IssueUpdateForeignBody403(t *testing.T) {
	h, ids := seedScope(t)
	ih := NewInternalIssueHandler(h.db, nil, testLogger())
	// Seed a ws-B issue so UpdateStatus has a real row to (not) mutate;
	// without it the 403 could be masked by a "not found" path.
	if _, err := h.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, priority, number, identifier, mission_type, created_at, updated_at)
		VALUES ('iss_b1', ?, ?, ?, 'tr-b1', 'ws-b issue', 'BACKLOG', 'low', 1, 'PRE-1', 'issue', datetime('now'), datetime('now'))`,
		ids.wsB, ids.crewB, ids.agentB); err != nil {
		t.Fatalf("seed ws-B issue: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"workspace_id": ids.wsB, "status": "TODO", "priority": "high"})
	rr := httptest.NewRecorder()
	req := setPathValue(boundReq(http.MethodPatch, "/x", body, scopeMaster, ids.wsA), "identifier", "PRE-1")
	h.requireInternal(http.HandlerFunc(ih.UpdateStatus)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	// The seeded row must be untouched — the body's status=TODO and
	// priority=high must not have landed; the seeded BACKLOG/low remains.
	var status, priority string
	if err := h.db.QueryRow(`SELECT status, COALESCE(priority,'') FROM missions WHERE id = 'iss_b1'`).Scan(&status, &priority); err != nil {
		t.Fatalf("read ws-B issue: %v", err)
	}
	if status != "BACKLOG" || priority != "low" {
		t.Fatalf("ws-B issue mutated cross-tenant: status=%q priority=%q, want BACKLOG/low", status, priority)
	}
}

func TestSecBinding_F4_MissionCreateForeignBody403(t *testing.T) {
	h, ids := seedScope(t)
	mh := NewInternalMissionHandler(h.db, nil, nil, testLogger())
	before := scopeCountRows(t, h, "missions")
	body, _ := json.Marshal(map[string]string{
		"workspace_id": ids.wsB, "crew_id": ids.crewB, "lead_agent_id": ids.agentB, "title": "x",
	})
	rr := httptest.NewRecorder()
	req := boundReq(http.MethodPost, "/x", body, scopeMaster, ids.wsA)
	h.requireInternal(http.HandlerFunc(mh.Create)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if after := scopeCountRows(t, h, "missions"); after != before {
		t.Fatalf("mission row written despite 403: count %d→%d", before, after)
	}
}

// ---------------------------------------------------------------------------
// F-5 MEDIUM — path-param mutations must not touch a foreign-tenant row.
// ws-A token incrementing ws-B's chat message count → 404, row untouched.
// ---------------------------------------------------------------------------
func TestSecBinding_F5_IncrementMessageCountCrossTenant(t *testing.T) {
	h, ids := seedScope(t)
	body := []byte(`{"delta":5}`)
	rr := httptest.NewRecorder()
	req := setPathValue(boundReq(http.MethodPatch, "/x", body, scopeMaster, ids.wsA), "chatId", ids.chatB)
	h.requireInternal(http.HandlerFunc(h.IncrementMessageCount)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (ws-A token must not bump ws-B chat); body=%s",
			rr.Code, rr.Body.String())
	}
	var mc int
	if err := h.db.QueryRow(`SELECT COALESCE(message_count,0) FROM chats WHERE id = ?`, ids.chatB).Scan(&mc); err != nil {
		t.Fatalf("read message_count: %v", err)
	}
	if mc != 0 {
		t.Fatalf("ws-B chat message_count mutated cross-tenant: got %d, want 0", mc)
	}
}

func TestSecBinding_F5_UpdateChatTitleCrossTenant(t *testing.T) {
	h, ids := seedScope(t)
	body := []byte(`{"title":"pwned"}`)
	rr := httptest.NewRecorder()
	req := setPathValue(boundReq(http.MethodPatch, "/x", body, scopeMaster, ids.wsA), "chatId", ids.chatB)
	h.requireInternal(http.HandlerFunc(h.UpdateChatTitle)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	var title string
	if err := h.db.QueryRow(`SELECT COALESCE(title,'') FROM chats WHERE id = ?`, ids.chatB).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title == "pwned" {
		t.Fatalf("ws-B chat title mutated cross-tenant")
	}
}

func TestSecBinding_F5_UpdateCredentialStatusCrossTenant(t *testing.T) {
	h, ids := seedScope(t)
	body := []byte(`{"status":"REVOKED"}`)
	rr := httptest.NewRecorder()
	req := setPathValue(boundReq(http.MethodPatch, "/x", body, scopeMaster, ids.wsA), "credentialId", "cred_b")
	h.requireInternal(http.HandlerFunc(h.UpdateCredentialStatus)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (ws-A token must not revoke ws-B credential); body=%s",
			rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM credentials WHERE id = ?`, "cred_b").Scan(&status); err != nil {
		t.Fatalf("read cred status: %v", err)
	}
	if status != "ACTIVE" {
		t.Fatalf("ws-B credential status mutated cross-tenant: got %q, want ACTIVE", status)
	}
}

// ---------------------------------------------------------------------------
// F-6 — a master token presented from a non-loopback (bridge) origin is
// refused. Pre-fix a leaked master from inside a container retained full
// cross-tenant reach.
// ---------------------------------------------------------------------------
func TestSecBinding_F6_MasterTokenFromBridgeRefused(t *testing.T) {
	t.Setenv("CREWSHIP_INTERNAL_ALLOW_ANY", "")
	h, _ := seedScope(t)
	reached := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "172.17.0.9:4444" // Docker bridge — a container origin
	req.Header.Set("X-Internal-Token", scopeMaster)
	rr := httptest.NewRecorder()
	h.requireInternal(downstream).ServeHTTP(rr, req)
	// Pin the specific refusal path: F-6 is the loopback-pin on a master
	// token that already cleared the private-network gate, so the response
	// must be exactly 403 (a 404 would mean the network gate fired instead,
	// testing a different control). The downstream handler must never run.
	if rr.Code != http.StatusForbidden || reached {
		t.Fatalf("bridge-origin master token must produce HTTP 403 without reaching downstream "+
			"(got status=%d, reached=%v); a leaked master inside a container must not authorize",
			rr.Code, reached)
	}
}
