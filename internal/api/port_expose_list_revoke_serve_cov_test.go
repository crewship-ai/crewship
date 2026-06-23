package api

// Coverage tests for port_expose_list_revoke_serve.go — List filters and
// nullable-column mapping, Revoke guard/error branches, and the ServeExposed
// docker re-resolve + proxy-error paths.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// covPEHandler builds a PortExposeHandler over the minimal schema DB used by
// the rest of the port-expose suite.
func covPEHandler(t *testing.T, db *sql.DB, docker DockerInspector, hub *ws.Hub) *PortExposeHandler {
	t.Helper()
	reg := NewPortExposeRegistry(db, portExposeTestLogger())
	return NewPortExposeHandler(db, reg, docker, AllowAllPolicy{}, hub, DefaultPortExposeConfig(), portExposeTestLogger())
}

func covPESeedExposure(t *testing.T, db *sql.DB, id, wsID, crewID, agentID, token, status string, revokedReason any) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO port_exposures (id, workspace_id, crew_id, agent_id, token,
			container_id, container_ip, container_port, description, status,
			expires_at, revoked_at, revoked_reason)
		VALUES (?, ?, ?, ?, ?, 'c1', '10.0.0.5', 3000, 'demo app', ?, ?, ?, ?)`,
		id, wsID, crewID, agentID, token, status,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		map[string]any{"REVOKED": time.Now().UTC().Format(time.RFC3339)}[status],
		revokedReason,
	); err != nil {
		t.Fatalf("seed exposure: %v", err)
	}
}

func covPEListReq(wsID, crewID, query string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/"+crewID+"/port-expose"+query, nil)
	if crewID != "" {
		req.SetPathValue("crewId", crewID)
	}
	return req.WithContext(withWorkspace(req.Context(), wsID, "MEMBER"))
}

// --- List ---------------------------------------------------------------------

func TestCovPEList_MissingCrewID400(t *testing.T) {
	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, nil, nil)
	rec := httptest.NewRecorder()
	h.List(rec, covPEListReq("ws1", "", ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCovPEList_BadStatusFilter400(t *testing.T) {
	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, nil, nil)
	rec := httptest.NewRecorder()
	h.List(rec, covPEListReq("ws1", "crew1", "?status=bogus"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "status must be") {
		t.Errorf("missing explanatory error: %s", rec.Body.String())
	}
}

func TestCovPEList_RevokedRowMapsNullableColumns(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	covPESeedExposure(t, db, "pe1", "ws1", "crew1", "agent1", "tok-1", "REVOKED", "no longer needed")
	h := covPEHandler(t, db, nil, nil)

	rec := httptest.NewRecorder()
	h.List(rec, covPEListReq("ws1", "crew1", "?status=revoked"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var out []listItem
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	it := out[0]
	if it.Description != "demo app" {
		t.Errorf("description = %q", it.Description)
	}
	if it.RevokedAt == nil || *it.RevokedAt == "" {
		t.Error("revoked_at not mapped")
	}
	if it.RevokedReason == nil || *it.RevokedReason != "no longer needed" {
		t.Errorf("revoked_reason = %v", it.RevokedReason)
	}
	if it.AgentSlug != "viktor" {
		t.Errorf("agent_slug = %q", it.AgentSlug)
	}
}

func TestCovPEList_AllFilterIncludesEverything(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	covPESeedExposure(t, db, "pe1", "ws1", "crew1", "agent1", "tok-a", "ACTIVE", nil)
	covPESeedExposure(t, db, "pe2", "ws1", "crew1", "agent1", "tok-r", "REVOKED", nil)
	h := covPEHandler(t, db, nil, nil)

	rec := httptest.NewRecorder()
	h.List(rec, covPEListReq("ws1", "crew1", "?status=all"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []listItem
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len = %d, want 2 (status=all)", len(out))
	}
}

func TestCovPEList_DBError500(t *testing.T) {
	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, nil, nil)
	db.Close()
	rec := httptest.NewRecorder()
	h.List(rec, covPEListReq("ws1", "crew1", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- Revoke -------------------------------------------------------------------

func covPERevokeReq(wsID, role, crewID, exposeID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/crews/"+crewID+"/port-expose/"+exposeID+"/revoke", strings.NewReader(body))
	if crewID != "" {
		req.SetPathValue("crewId", crewID)
	}
	if exposeID != "" {
		req.SetPathValue("id", exposeID)
	}
	return req.WithContext(withWorkspace(req.Context(), wsID, role))
}

func TestCovPERevoke_Guards(t *testing.T) {
	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, nil, nil)

	t.Run("forbidden", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Revoke(rec, covPERevokeReq("ws1", "VIEWER", "crew1", "pe1", `{}`))
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("missing ids 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Revoke(rec, covPERevokeReq("ws1", "ADMIN", "", "", `{}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("reason too long 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		long := strings.Repeat("x", 501)
		h.Revoke(rec, covPERevokeReq("ws1", "ADMIN", "crew1", "pe1", `{"reason":"`+long+`"}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("not found conflict 409", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Revoke(rec, covPERevokeReq("ws1", "ADMIN", "crew1", "missing", `{}`))
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rec.Code)
		}
	})
}

func TestCovPERevoke_SuccessWithHubBroadcast(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	covPESeedExposure(t, db, "pe1", "ws1", "crew1", "agent1", "tok-live", "ACTIVE", nil)
	hub := ws.NewHub(portExposeTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	h := covPEHandler(t, db, nil, hub)
	// Plant the token in the registry so the Remove branch executes.
	h.registry.Add(&ExposeEntry{Token: "tok-live", ContainerIP: "10.0.0.5", ContainerPort: 3000, ExpiresAt: time.Now().Add(time.Hour)})

	rec := httptest.NewRecorder()
	h.Revoke(rec, covPERevokeReq("ws1", "ADMIN", "crew1", "pe1", `{"reason":"done testing"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var status, reason string
	if err := db.QueryRow(`SELECT status, revoked_reason FROM port_exposures WHERE id = 'pe1'`).Scan(&status, &reason); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "REVOKED" || reason != "done testing" {
		t.Errorf("row = (%s, %s), want (REVOKED, done testing)", status, reason)
	}
	if _, ok := h.registry.Lookup("tok-live"); ok {
		t.Error("token should be removed from registry")
	}
}

func TestCovPERevoke_DBError500(t *testing.T) {
	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, nil, nil)
	db.Close()
	rec := httptest.NewRecorder()
	h.Revoke(rec, covPERevokeReq("ws1", "ADMIN", "crew1", "pe1", `{}`))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- ServeExposed --------------------------------------------------------------

func TestCovPEServeExposed_EmptyToken404(t *testing.T) {
	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/exposed/", nil)
	rec := httptest.NewRecorder()
	h.ServeExposed(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestCovPEServeExposed_DockerReResolve covers the docker-wired branch: the
// inspector reports a fresh IP differing from the cached one; the registry
// cache must be updated and the proxy must hit the fresh address.
func TestCovPEServeExposed_DockerReResolve(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Got-Path", r.URL.Path)
		_, _ = w.Write([]byte("fresh"))
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	host, portStr := u.Hostname(), u.Port()
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, &fakeDockerInspector{ip: host}, nil)
	h.registry.Add(&ExposeEntry{
		Token:         "tok-reresolve",
		ContainerID:   "c-9",
		ContainerIP:   "192.0.2.99", // stale cached IP
		ContainerPort: port,
		ExpiresAt:     time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodGet, "/exposed/tok-reresolve", nil)
	req.SetPathValue("token", "tok-reresolve")
	rec := httptest.NewRecorder()
	h.ServeExposed(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "fresh" {
		t.Errorf("body = %q, want fresh", rec.Body.String())
	}
	// Path without suffix must be rewritten to "/".
	if got := rec.Header().Get("X-Got-Path"); got != "/" {
		t.Errorf("upstream path = %q, want /", got)
	}
	// Cached IP updated.
	if e, ok := h.registry.Lookup("tok-reresolve"); !ok || e.ContainerIP != host {
		t.Errorf("registry IP not refreshed: %+v", e)
	}
}

func TestCovPEServeExposed_DockerResolveFails502(t *testing.T) {
	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, &fakeDockerInspector{err: errors.New("no such container")}, nil)
	h.registry.Add(&ExposeEntry{
		Token:         "tok-fail",
		ContainerID:   "c-9",
		ContainerIP:   "192.0.2.99",
		ContainerPort: 3000,
		ExpiresAt:     time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodGet, "/exposed/tok-fail/", nil)
	req.SetPathValue("token", "tok-fail")
	rec := httptest.NewRecorder()
	h.ServeExposed(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

// TestCovPEServeExposed_ProxyError502 drives the reverse-proxy ErrorHandler:
// the upstream is closed before the request, so the dial fails and the
// handler must answer 502 instead of crashing.
func TestCovPEServeExposed_ProxyError502(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u, _ := url.Parse(upstream.URL)
	host, portStr := u.Hostname(), u.Port()
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	upstream.Close() // free the port — dial now fails

	db := newHandlerTestDB(t)
	h := covPEHandler(t, db, nil, nil)
	h.registry.Add(&ExposeEntry{
		Token:         "tok-dead",
		ContainerIP:   host,
		ContainerPort: port,
		ExpiresAt:     time.Now().Add(time.Hour),
	})

	req := httptest.NewRequest(http.MethodGet, "/exposed/tok-dead/x", nil)
	req.SetPathValue("token", "tok-dead")
	rec := httptest.NewRecorder()
	h.ServeExposed(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}
