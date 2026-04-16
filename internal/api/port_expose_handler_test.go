package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newHandlerTestDB is a richer version of newRegistryTestDB that also
// provisions the surrounding tables RequestExpose / List / Revoke touch
// (workspaces, crews, agents). FK checks stay off because tests plant
// minimal parent rows, not a full object graph.
func newHandlerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Parent tables. Only the columns the handlers join on are modelled.
	stmts := []string{
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY);`,
		`CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT, deleted_at TEXT);`,
		`CREATE TABLE agents (id TEXT PRIMARY KEY, slug TEXT, crew_id TEXT, deleted_at TEXT);`,
		`CREATE TABLE chats (id TEXT PRIMARY KEY);`,
		`CREATE TABLE port_exposures (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			crew_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			chat_id TEXT,
			token TEXT NOT NULL UNIQUE,
			container_id TEXT NOT NULL,
			container_ip TEXT NOT NULL,
			container_port INTEGER NOT NULL,
			description TEXT,
			status TEXT NOT NULL DEFAULT 'ACTIVE',
			expires_at TEXT NOT NULL,
			revoked_at TEXT,
			revoked_reason TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

// planWorkspace wires up a workspace+crew+agent tuple so RequestExpose passes
// the boundary check. All tests that touch the handler need at least this.
func planWorkspace(t *testing.T, db *sql.DB, wsID, crewID, agentID, agentSlug string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES (?)`, wsID); err != nil {
		t.Fatalf("insert ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id) VALUES (?, ?)`, crewID, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, slug, crew_id) VALUES (?, ?, ?)`, agentID, agentSlug, crewID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
}

// fakeDockerInspector always returns the same IP unless told to fail.
type fakeDockerInspector struct {
	ip  string
	err error
}

func (f *fakeDockerInspector) ContainerIP(_ context.Context, _, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.ip, nil
}

// newRequestExposeHandler builds a handler with the given config and one
// fake docker inspector. Tests call h.RequestExpose directly, so we don't
// bother wiring a mux. PublicBaseURL is set so the config gate passes.
func newRequestExposeHandler(t *testing.T, db *sql.DB, cfg PortExposeConfig, docker DockerInspector) *PortExposeHandler {
	t.Helper()
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = "http://test.local:8080"
	}
	reg := NewPortExposeRegistry(db, slog.Default())
	return NewPortExposeHandler(db, reg, docker, AllowAllPolicy{}, nil, cfg, slog.Default())
}

func postJSON(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/port-expose", strings.NewReader(string(buf)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestRequestExpose_Happy(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	h := newRequestExposeHandler(t, db, DefaultPortExposeConfig(), &fakeDockerInspector{ip: "10.0.0.2"})

	rec := postJSON(t, h.RequestExpose, map[string]any{
		"workspace_id": "ws1",
		"crew_id":      "crew1",
		"agent_id":     "agent1",
		"container_id": "c1",
		"port":         8000,
		"description":  "demo",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got requestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Token == "" || got.URL == "" || got.ID == "" {
		t.Errorf("response missing fields: %+v", got)
	}
	if !strings.Contains(got.URL, "/exposed/"+got.Token+"/") {
		t.Errorf("url %q does not contain token path", got.URL)
	}

	// DB row should be ACTIVE.
	var status string
	if err := db.QueryRow(`SELECT status FROM port_exposures WHERE id = ?`, got.ID).Scan(&status); err != nil {
		t.Fatalf("fetch row: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", status)
	}

	// Registry should carry the same token.
	if _, ok := h.registry.Lookup(got.Token); !ok {
		t.Errorf("registry missing inserted token")
	}
}

func TestRequestExpose_RefusesWithoutPublicBaseURL(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	cfg := DefaultPortExposeConfig()
	cfg.PublicBaseURL = ""
	h := newRequestExposeHandler(t, db, cfg, &fakeDockerInspector{ip: "10.0.0.2"})
	// Force empty after helper auto-default
	h.cfg.PublicBaseURL = ""

	rec := postJSON(t, h.RequestExpose, map[string]any{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
		"container_id": "c1", "port": 8000,
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CREWSHIP_PUBLIC_URL") {
		t.Errorf("body should mention env var: %s", rec.Body.String())
	}
}

func TestRequestExpose_RejectsBadPort(t *testing.T) {
	db := newHandlerTestDB(t)
	h := newRequestExposeHandler(t, db, DefaultPortExposeConfig(), &fakeDockerInspector{ip: "10.0.0.2"})

	rec := postJSON(t, h.RequestExpose, map[string]any{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
		"container_id": "c1", "port": 0,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("port=0 should be 400, got %d", rec.Code)
	}
}

func TestRequestExpose_AgentBoundaryMismatch(t *testing.T) {
	db := newHandlerTestDB(t)
	// Agent belongs to crew1, but request says crew2 (which exists in a different workspace).
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('ws1'), ('ws2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id) VALUES ('crew1','ws1'), ('crew2','ws2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, slug, crew_id) VALUES ('a1','viktor','crew1')`); err != nil {
		t.Fatal(err)
	}
	h := newRequestExposeHandler(t, db, DefaultPortExposeConfig(), &fakeDockerInspector{ip: "10.0.0.2"})

	rec := postJSON(t, h.RequestExpose, map[string]any{
		"workspace_id": "ws2",  // wrong — agent is in ws1
		"crew_id":      "crew2",
		"agent_id":     "a1",
		"container_id": "c1",
		"port":         8000,
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequestExpose_ContainerNotOnNetwork(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	h := newRequestExposeHandler(t, db, DefaultPortExposeConfig(),
		&fakeDockerInspector{err: fmt.Errorf("container not on network")})

	rec := postJSON(t, h.RequestExpose, map[string]any{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
		"container_id": "c1", "port": 8000,
	})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequestExpose_QuotaExceededPerAgent(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	cfg := DefaultPortExposeConfig()
	cfg.MaxActivePerAgent = 1
	h := newRequestExposeHandler(t, db, cfg, &fakeDockerInspector{ip: "10.0.0.2"})

	// Pre-insert one ACTIVE row so the next request trips the quota.
	_, err := db.Exec(`
		INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
		VALUES ('existing','ws1','crew1','agent1','t-existing','c1','10.0.0.2',9000,'ACTIVE',?)
	`, time.Now().Add(time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed existing: %v", err)
	}

	rec := postJSON(t, h.RequestExpose, map[string]any{
		"workspace_id": "ws1", "crew_id": "crew1", "agent_id": "agent1",
		"container_id": "c1", "port": 8000,
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
}

func TestList_FilterByStatusAndScope(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	h := newRequestExposeHandler(t, db, DefaultPortExposeConfig(), &fakeDockerInspector{ip: "10.0.0.2"})

	ins := func(id, status string) {
		t.Helper()
		_, err := db.Exec(`
			INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, "ws1", "crew1", "agent1", "tok-"+id, "c1", "10.0.0.2", 8080, status,
			time.Now().Add(time.Hour).Format(time.RFC3339))
		if err != nil {
			t.Fatal(err)
		}
	}
	ins("a", "ACTIVE")
	ins("r", "REVOKED")

	// Fabricate a request with workspace + role in context, path param crewId.
	call := func(statusQ string) *httptest.ResponseRecorder {
		u := "/api/v1/crews/crew1/port-expose"
		if statusQ != "" {
			u += "?status=" + statusQ
		}
		req := httptest.NewRequest(http.MethodGet, u, nil)
		ctx := context.WithValue(req.Context(), ctxWorkspaceID, "ws1")
		_ = ctx
		req = req.WithContext(ctx)
		req.SetPathValue("crewId", "crew1")
		rec := httptest.NewRecorder()
		h.List(rec, req)
		return rec
	}

	rec := call("") // default = active
	if rec.Code != http.StatusOK {
		t.Fatalf("default list: %d body=%s", rec.Code, rec.Body.String())
	}
	var active []listItem
	_ = json.Unmarshal(rec.Body.Bytes(), &active)
	if len(active) != 1 || active[0].ID != "a" {
		t.Errorf("default should return 1 ACTIVE, got %+v", active)
	}

	rec = call("revoked")
	var revoked []listItem
	_ = json.Unmarshal(rec.Body.Bytes(), &revoked)
	if len(revoked) != 1 || revoked[0].ID != "r" {
		t.Errorf("revoked filter: %+v", revoked)
	}

	rec = call("all")
	var all []listItem
	_ = json.Unmarshal(rec.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("all filter count: got %d, want 2", len(all))
	}
}

func TestRevoke_RoleCheckAndStatusTransition(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	h := newRequestExposeHandler(t, db, DefaultPortExposeConfig(), &fakeDockerInspector{ip: "10.0.0.2"})

	_, err := db.Exec(`
		INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token, container_id, container_ip, container_port, status, expires_at)
		VALUES ('e1','ws1','crew1','agent1','tok1','c1','10.0.0.2',8000,'ACTIVE',?)`,
		time.Now().Add(time.Hour).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	h.registry.Add(&ExposeEntry{Token: "tok1", ContainerIP: "10.0.0.2", ContainerPort: 8000,
		ExpiresAt: time.Now().Add(time.Hour)})

	// MEMBER (read-only) cannot revoke.
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/crews/crew1/port-expose/e1/revoke", nil)
		ctx := context.WithValue(req.Context(), ctxWorkspaceID, "ws1")
		ctx = context.WithValue(ctx, ctxRole, "MEMBER")
		req = req.WithContext(ctx)
		req.SetPathValue("crewId", "crew1")
		req.SetPathValue("id", "e1")
		rec := httptest.NewRecorder()
		h.Revoke(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("MEMBER revoke should 403, got %d", rec.Code)
		}
	}

	// MANAGER can revoke.
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/crews/crew1/port-expose/e1/revoke",
			strings.NewReader(`{"reason":"tidy up"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), ctxWorkspaceID, "ws1")
		ctx = context.WithValue(ctx, ctxRole, "MANAGER")
		req = req.WithContext(ctx)
		req.SetPathValue("crewId", "crew1")
		req.SetPathValue("id", "e1")
		rec := httptest.NewRecorder()
		h.Revoke(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("MANAGER revoke should 200, got %d body=%s", rec.Code, rec.Body.String())
		}
	}

	// Row must be REVOKED and registry must be empty for the token.
	var status string
	var reason sql.NullString
	if err := db.QueryRow(`SELECT status, revoked_reason FROM port_exposures WHERE id='e1'`).Scan(&status, &reason); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "REVOKED" {
		t.Errorf("status = %q, want REVOKED", status)
	}
	if !reason.Valid || reason.String != "tidy up" {
		t.Errorf("reason = %v, want 'tidy up'", reason)
	}
	if _, ok := h.registry.Lookup("tok1"); ok {
		t.Errorf("registry should have dropped token after revoke")
	}

	// Second revoke on same row returns 409 (already revoked).
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/crews/crew1/port-expose/e1/revoke", nil)
		ctx := context.WithValue(req.Context(), ctxWorkspaceID, "ws1")
		ctx = context.WithValue(ctx, ctxRole, "MANAGER")
		req = req.WithContext(ctx)
		req.SetPathValue("crewId", "crew1")
		req.SetPathValue("id", "e1")
		rec := httptest.NewRecorder()
		h.Revoke(rec, req)
		if rec.Code != http.StatusConflict {
			t.Errorf("double revoke should 409, got %d", rec.Code)
		}
	}
}
