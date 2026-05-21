package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/memory"
)

type peerTestRig struct {
	h       *PeerCardHandler
	privacy *UserPeerPrivacyHandler
	db      *sql.DB
	wsID    string
	crewID  string
	agentID string
	userID  string
	output  string
}

// peerTestSetup wires the full peer + privacy + DB stack against a
// real sqlite. Seeds one workspace, one user, one crew, one agent
// — every test extends from there.
func peerTestSetup(t *testing.T) *peerTestRig {
	t.Helper()
	dir := t.TempDir()
	dbh, err := database.Open("file:" + filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), dbh.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })
	if _, err := dbh.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x'),('u2','u2@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES ('crew1','ws1','C','c','free','[]')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := dbh.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('a1','ws1','crew1','alice','Alice','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return &peerTestRig{
		h:       NewPeerCardHandler(dbh.DB, silent, dir),
		privacy: NewUserPeerPrivacyHandler(dbh.DB, silent, dir),
		db:      dbh.DB,
		wsID:    "ws1", crewID: "crew1", agentID: "a1", userID: "u1",
		output: dir,
	}
}

// seedCard writes both the disk file and the index row, mirroring
// what SyncPeerCard does so the agent/privacy endpoints have
// realistic state to operate on.
func (r *peerTestRig) seedCard(t *testing.T, userID, content string) {
	t.Helper()
	paths := memory.PeerPaths{
		AgentDir: filepath.Join(r.output, "crews", r.crewID, "agents", "alice", ".memory"),
	}
	if err := memory.WritePeerCard(paths, userID, r.wsID, content); err != nil {
		t.Fatalf("seed disk card: %v", err)
	}
	slug := memory.UserSlug(userID, r.wsID)
	if _, err := r.db.Exec(`
		INSERT INTO peer_cards (id, workspace_id, agent_id, user_id, user_slug, path, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "pc-"+userID, r.wsID, r.agentID, userID, slug, paths.CardPath(slug), len(content)); err != nil {
		t.Fatalf("seed db row: %v", err)
	}
}

func (r *peerTestRig) req(t *testing.T, method, body string, pathVals map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	for k, v := range pathVals {
		req.SetPathValue(k, v)
	}
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, r.wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: r.userID})
	return req.WithContext(ctx)
}

func TestPeers_ListAndGetAndDelete(t *testing.T) {
	r := peerTestSetup(t)
	r.seedCard(t, "u1", "Pavel notes")
	r.seedCard(t, "u2", "Ivana notes")

	// List → 2 entries.
	rec := httptest.NewRecorder()
	r.h.ListAgentPeers(rec, r.req(t, http.MethodGet, "", map[string]string{"agentId": r.agentID}))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got["peers"].([]any)) != 2 {
		t.Errorf("expected 2 peers; got %d", len(got["peers"].([]any)))
	}

	// Get u1 → content + audit row.
	rec = httptest.NewRecorder()
	r.h.GetAgentPeer(rec, r.req(t, http.MethodGet, "", map[string]string{
		"agentId": r.agentID, "userId": "u1",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["content"].(string) != "Pavel notes" {
		t.Errorf("expected 'Pavel notes'; got %q", got["content"])
	}
	var auditReadCnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit WHERE action='read' AND target_user_id='u1'`).Scan(&auditReadCnt); err != nil {
		t.Fatalf("count read audit: %v", err)
	}
	if auditReadCnt != 1 {
		t.Errorf("expected 1 read audit row; got %d", auditReadCnt)
	}

	// Delete u1.
	rec = httptest.NewRecorder()
	r.h.DeleteAgentPeer(rec, r.req(t, http.MethodDelete, "", map[string]string{
		"agentId": r.agentID, "userId": "u1",
	}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	// Index row gone.
	var cnt int
	r.db.QueryRow(`SELECT COUNT(*) FROM peer_cards WHERE user_id='u1'`).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("expected u1 row purged; got %d", cnt)
	}
	// Disk file gone.
	body, _ := memory.LoadPeerCard(memory.PeerPaths{AgentDir: filepath.Join(r.output, "crews", r.crewID, "agents", "alice", ".memory")}, "u1", r.wsID)
	if body != "" {
		t.Errorf("expected disk card purged; got %q", body)
	}
	// Delete-audit row landed.
	var delCnt int
	r.db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit WHERE action='delete' AND target_user_id='u1'`).Scan(&delCnt)
	if delCnt != 1 {
		t.Errorf("expected 1 delete audit; got %d", delCnt)
	}
}

func TestPeers_GetWithNoCardReturns404(t *testing.T) {
	r := peerTestSetup(t)
	rec := httptest.NewRecorder()
	r.h.GetAgentPeer(rec, r.req(t, http.MethodGet, "", map[string]string{
		"agentId": r.agentID, "userId": "u_nobody",
	}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404; got %d", rec.Code)
	}
}

func TestPrivacy_OptOutPurgesImmediately(t *testing.T) {
	r := peerTestSetup(t)
	r.seedCard(t, r.userID, "card about me")
	// Verify disk + DB present.
	var cnt int
	r.db.QueryRow(`SELECT COUNT(*) FROM peer_cards WHERE user_id=?`, r.userID).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("seed assert failed; got cnt=%d", cnt)
	}

	rec := httptest.NewRecorder()
	r.privacy.PutConsent(rec, r.req(t, http.MethodPut, `{"opted_out":true}`, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("opt out: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["opted_out"] != true {
		t.Errorf("expected opted_out=true in response; got %+v", got)
	}
	if int(got["purged"].(float64)) != 1 {
		t.Errorf("expected purged=1; got %v", got["purged"])
	}

	// DB row gone.
	r.db.QueryRow(`SELECT COUNT(*) FROM peer_cards WHERE user_id=?`, r.userID).Scan(&cnt)
	if cnt != 0 {
		t.Errorf("expected 0 cards after opt-out; got %d", cnt)
	}
	// consent row present.
	var opted int
	r.db.QueryRow(`SELECT opted_out FROM user_peer_consent WHERE user_id=? AND workspace_id=?`,
		r.userID, r.wsID).Scan(&opted)
	if opted != 1 {
		t.Errorf("expected consent.opted_out=1; got %d", opted)
	}
	// Audit rows: 1 opt_out + 1 delete (we record both).
	var optOutCnt, delCnt int
	r.db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit WHERE action='opt_out' AND target_user_id=?`, r.userID).Scan(&optOutCnt)
	r.db.QueryRow(`SELECT COUNT(*) FROM peer_card_audit WHERE action='delete' AND target_user_id=?`, r.userID).Scan(&delCnt)
	if optOutCnt != 1 || delCnt != 1 {
		t.Errorf("expected 1 opt_out + 1 delete audit; got %d / %d", optOutCnt, delCnt)
	}
}

func TestPrivacy_GetMyCards(t *testing.T) {
	r := peerTestSetup(t)
	r.seedCard(t, r.userID, "self profile")
	rec := httptest.NewRecorder()
	r.privacy.GetMyCards(rec, r.req(t, http.MethodGet, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get my: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	peers := got["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer card; got %d", len(peers))
	}
	first := peers[0].(map[string]any)
	if first["content"].(string) != "self profile" {
		t.Errorf("missing content in response; got %v", first)
	}
}

func TestPrivacy_DeleteMyCardsHonoursMultipleAgents(t *testing.T) {
	r := peerTestSetup(t)
	// Spin up a second agent in the same crew/workspace.
	if _, err := r.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
		VALUES ('a2','ws1','crew1','bob','Bob','AGENT')`); err != nil {
		t.Fatalf("seed a2: %v", err)
	}
	// Seed cards on both agents.
	r.seedCard(t, r.userID, "alice's view")
	// Manually seed for a2.
	paths := memory.PeerPaths{AgentDir: filepath.Join(r.output, "crews", r.crewID, "agents", "bob", ".memory")}
	if err := memory.WritePeerCard(paths, r.userID, r.wsID, "bob's view"); err != nil {
		t.Fatalf("write a2 card: %v", err)
	}
	slug := memory.UserSlug(r.userID, r.wsID)
	if _, err := r.db.Exec(`INSERT INTO peer_cards (id, workspace_id, agent_id, user_id, user_slug, path, bytes)
		VALUES ('pc-a2', ?, 'a2', ?, ?, ?, 10)`,
		r.wsID, r.userID, slug, paths.CardPath(slug)); err != nil {
		t.Fatalf("seed a2 row: %v", err)
	}

	rec := httptest.NewRecorder()
	r.privacy.DeleteMyCards(rec, r.req(t, http.MethodDelete, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if int(got["purged"].(float64)) != 2 {
		t.Errorf("expected purged=2 across agents; got %v", got["purged"])
	}

	// Neither file should remain.
	for _, slug := range []string{"alice", "bob"} {
		p := memory.PeerPaths{AgentDir: filepath.Join(r.output, "crews", r.crewID, "agents", slug, ".memory")}
		body, _ := memory.LoadPeerCard(p, r.userID, r.wsID)
		if body != "" {
			t.Errorf("expected card purged on agent %s; got %q", slug, body)
		}
	}
}

func TestPrivacy_GetConsentReturnsDefault(t *testing.T) {
	r := peerTestSetup(t)
	rec := httptest.NewRecorder()
	r.privacy.GetConsent(rec, r.req(t, http.MethodGet, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get consent: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["opted_out"] != false {
		t.Errorf("default consent should be opted_out=false; got %v", got["opted_out"])
	}
}
