package api

// Phase-2 coverage top-up for agent_peers.go and hooks_handler.go.
//
// These exercise the error/edge branches the existing
// agent_peers_test.go / hooks_handler_test.go leave uncovered:
//
//   agent_peers.go
//     - ListAgentPeers: missing workspace ctx (401), agent not found
//       (404), agent w/o crew (409), list query failure (500)
//     - GetAgentPeer:   storage not configured (503), agent not found
//       (404), agent w/o crew (409)
//     - DeleteAgentPeer: storage not configured (503), agent not found
//       (404), agent w/o crew (409), idempotent delete of a missing
//       card (204)
//
//   hooks_handler.go
//     - List: missing workspace ctx (401), crew_id filter happy path,
//       crew_id belonging to another workspace (404), crew lookup
//       failure (500), list query failure (500)
//     - setEnabled (via Enable/Disable): missing workspace ctx (401),
//       empty id (400), lookup failure (500)
//
// SKIPPED: no network/Docker branches exist in either file — the peer
// handlers touch only the local filesystem + sqlite, and the hooks
// handlers are pure DB. GetAgentPeer / DeleteAgentPeer happy paths and
// the audit-row write are already covered by agent_peers_test.go, so we
// only add the error legs here.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
)

// covPH2PeerHandler builds a PeerCardHandler with storage configured at
// a temp dir, plus a seeded workspace/crew/agent. Returns the handler,
// the db, and the seeded ids.
func covPH2PeerHandler(t *testing.T) (h *PeerCardHandler, db *sql.DB, userID, wsID, agentID string) {
	t.Helper()
	db = setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-ph2", wsID, "Crew", "crew")
	agentID = seedAgentRow(t, db, "agent-ph2", wsID, "crew-ph2", "Alice", "alice", "AGENT")
	h = NewPeerCardHandler(db, newTestLogger(), t.TempDir())
	return h, db, userID, wsID, agentID
}

// covPH2PeerReq builds a request with workspace + user context and the
// supplied path values. wsID="" omits the workspace value entirely so
// the missing-context branch can be exercised.
func covPH2PeerReq(method, wsID, userID string, pathVals map[string]string) *http.Request {
	req := httptest.NewRequest(method, "/", nil)
	for k, v := range pathVals {
		req.SetPathValue(k, v)
	}
	ctx := req.Context()
	if wsID != "" {
		ctx = context.WithValue(ctx, ctxWorkspaceID, wsID)
	}
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	return req.WithContext(ctx)
}

// --------------------------------------------------------------------------
// agent_peers.go — ListAgentPeers
// --------------------------------------------------------------------------

func TestCovPH2ListAgentPeers_MissingWorkspace(t *testing.T) {
	h, _, userID, _, agentID := covPH2PeerHandler(t)
	rec := httptest.NewRecorder()
	h.ListAgentPeers(rec, covPH2PeerReq(http.MethodGet, "", userID, map[string]string{"agentId": agentID}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing workspace: code = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2ListAgentPeers_AgentNotFound(t *testing.T) {
	h, _, userID, wsID, _ := covPH2PeerHandler(t)
	rec := httptest.NewRecorder()
	h.ListAgentPeers(rec, covPH2PeerReq(http.MethodGet, wsID, userID, map[string]string{"agentId": "nope"}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent: code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2ListAgentPeers_AgentNoCrew(t *testing.T) {
	h, db, userID, wsID, _ := covPH2PeerHandler(t)
	// Solo agent — no crew_id — must map to 409 Conflict.
	solo := seedAgentRow(t, db, "agent-solo", wsID, "", "Solo", "solo", "COORDINATOR")
	rec := httptest.NewRecorder()
	h.ListAgentPeers(rec, covPH2PeerReq(http.MethodGet, wsID, userID, map[string]string{"agentId": solo}))
	if rec.Code != http.StatusConflict {
		t.Errorf("crewless agent: code = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2ListAgentPeers_QueryError500(t *testing.T) {
	h, db, userID, wsID, agentID := covPH2PeerHandler(t)
	// resolveAgent runs first and would itself fail on a closed DB, but
	// it returns a generic error → 500, which is the branch we want to
	// confirm degrades safely rather than panicking.
	db.Close()
	rec := httptest.NewRecorder()
	h.ListAgentPeers(rec, covPH2PeerReq(http.MethodGet, wsID, userID, map[string]string{"agentId": agentID}))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("closed db: code = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}

// --------------------------------------------------------------------------
// agent_peers.go — GetAgentPeer
// --------------------------------------------------------------------------

func TestCovPH2GetAgentPeer_StorageNotConfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-ph2", wsID, "Crew", "crew")
	agentID := seedAgentRow(t, db, "agent-ph2", wsID, "crew-ph2", "Alice", "alice", "AGENT")
	// Empty outputBasePath → 503.
	h := NewPeerCardHandler(db, newTestLogger(), "")
	rec := httptest.NewRecorder()
	h.GetAgentPeer(rec, covPH2PeerReq(http.MethodGet, wsID, userID, map[string]string{
		"agentId": agentID, "userId": "u1",
	}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no storage: code = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2GetAgentPeer_AgentNotFound(t *testing.T) {
	h, _, userID, wsID, _ := covPH2PeerHandler(t)
	rec := httptest.NewRecorder()
	h.GetAgentPeer(rec, covPH2PeerReq(http.MethodGet, wsID, userID, map[string]string{
		"agentId": "ghost", "userId": "u1",
	}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent: code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2GetAgentPeer_AgentNoCrew(t *testing.T) {
	h, db, userID, wsID, _ := covPH2PeerHandler(t)
	solo := seedAgentRow(t, db, "agent-solo", wsID, "", "Solo", "solo", "COORDINATOR")
	rec := httptest.NewRecorder()
	h.GetAgentPeer(rec, covPH2PeerReq(http.MethodGet, wsID, userID, map[string]string{
		"agentId": solo, "userId": "u1",
	}))
	if rec.Code != http.StatusConflict {
		t.Errorf("crewless agent: code = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

// --------------------------------------------------------------------------
// agent_peers.go — DeleteAgentPeer
// --------------------------------------------------------------------------

func TestCovPH2DeleteAgentPeer_StorageNotConfigured(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-ph2", wsID, "Crew", "crew")
	agentID := seedAgentRow(t, db, "agent-ph2", wsID, "crew-ph2", "Alice", "alice", "AGENT")
	h := NewPeerCardHandler(db, newTestLogger(), "")
	rec := httptest.NewRecorder()
	h.DeleteAgentPeer(rec, covPH2PeerReq(http.MethodDelete, wsID, userID, map[string]string{
		"agentId": agentID, "userId": "u1",
	}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no storage: code = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2DeleteAgentPeer_AgentNotFound(t *testing.T) {
	h, _, userID, wsID, _ := covPH2PeerHandler(t)
	rec := httptest.NewRecorder()
	h.DeleteAgentPeer(rec, covPH2PeerReq(http.MethodDelete, wsID, userID, map[string]string{
		"agentId": "ghost", "userId": "u1",
	}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent: code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2DeleteAgentPeer_AgentNoCrew(t *testing.T) {
	h, db, userID, wsID, _ := covPH2PeerHandler(t)
	solo := seedAgentRow(t, db, "agent-solo", wsID, "", "Solo", "solo", "COORDINATOR")
	rec := httptest.NewRecorder()
	h.DeleteAgentPeer(rec, covPH2PeerReq(http.MethodDelete, wsID, userID, map[string]string{
		"agentId": solo, "userId": "u1",
	}))
	if rec.Code != http.StatusConflict {
		t.Errorf("crewless agent: code = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2DeleteAgentPeer_IdempotentMissingCard(t *testing.T) {
	h, db, userID, wsID, agentID := covPH2PeerHandler(t)
	// The agent's peers/ dir must exist for the file lock to open, but
	// the card file itself is absent — DeletePeerCard removes a missing
	// file cleanly (os.Remove tolerates fs.ErrNotExist), so the handler
	// still returns 204 and writes a delete audit row.
	peersDir := h.agentPeerDir("crew-ph2", "alice").PeersDir()
	if err := os.MkdirAll(peersDir, 0o755); err != nil {
		t.Fatalf("mkdir peers: %v", err)
	}
	// The audit row's target_user_id has a FK to users(id), so the
	// target must be a real user even though no card exists for it.
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u-noCard', 'nocard@example.com')`); err != nil {
		t.Fatalf("seed target user: %v", err)
	}
	rec := httptest.NewRecorder()
	h.DeleteAgentPeer(rec, covPH2PeerReq(http.MethodDelete, wsID, userID, map[string]string{
		"agentId": agentID, "userId": "u-noCard",
	}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("idempotent delete: code = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	var delCnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit WHERE action='delete' AND target_user_id='u-noCard'`).Scan(&delCnt); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if delCnt != 1 {
		t.Errorf("expected 1 delete audit row even for missing card; got %d", delCnt)
	}
}

// --------------------------------------------------------------------------
// hooks_handler.go — List
// --------------------------------------------------------------------------

func TestCovPH2HooksList_MissingWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks", nil)
	// No workspace context.
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing workspace: code = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2HooksList_CrewFilterHappy(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-hk", wsID, "Crew", "crew")

	// One crew-scoped hook + one workspace-wide hook; both should appear
	// for a crew-filtered list (WHERE crew_id IS NULL OR crew_id = ?).
	if _, err := hooks.Register(context.Background(), db, hooks.Hook{
		WorkspaceID: wsID, CrewID: crewID, Event: hooks.EventPostToolCall,
		HandlerKind: hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "http://x.test/h"}, Enabled: true,
	}, false); err != nil {
		t.Fatalf("register crew hook: %v", err)
	}
	if _, err := hooks.Register(context.Background(), db, hooks.Hook{
		WorkspaceID: wsID, Event: hooks.EventPreToolCall,
		HandlerKind: hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "http://x.test/g"}, Enabled: true,
	}, false); err != nil {
		t.Fatalf("register ws hook: %v", err)
	}

	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks?crew_id="+crewID, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("crew filter: code = %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Rows  []hookRow `json:"rows"`
		Count int       `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 2 {
		t.Errorf("crew filter count = %d, want 2 (crew + ws-wide)", resp.Count)
	}
}

func TestCovPH2HooksList_CrewCrossTenant404(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Crew that belongs to a different workspace.
	otherWS := "other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	foreignCrew := seedCrewRow(t, db, "crew-foreign", otherWS, "Foreign", "foreign")

	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks?crew_id="+foreignCrew, nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant crew filter: code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2HooksList_CrewLookupError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close() // crewBelongsToWorkspace query fails → 500.
	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks?crew_id=whatever", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("crew lookup on closed db: code = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2HooksList_QueryError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close() // no crew filter → the main SELECT fails → 500.
	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/hooks", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("list query on closed db: code = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}

// --------------------------------------------------------------------------
// hooks_handler.go — setEnabled (via Enable / Disable)
// --------------------------------------------------------------------------

func TestCovPH2HooksEnable_MissingWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/x/enable", nil)
	req.SetPathValue("id", "x")
	// No workspace context.
	rec := httptest.NewRecorder()
	h.Enable(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing workspace: code = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2HooksEnable_EmptyID(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks//enable", nil)
	// No id path value set → empty → 400.
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Enable(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty id: code = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestCovPH2HooksDisable_LookupError500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	db.Close() // hooks.Get fails → "lookup failed" 500.
	h := NewHooksHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/some-id/disable", nil)
	req.SetPathValue("id", "some-id")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Disable(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("lookup on closed db: code = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
}
