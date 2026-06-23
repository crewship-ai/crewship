package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// port_expose_request_cov_test.go covers the remaining RequestExpose
// branches: bad JSON, long description, boundary-check 500, TTL clamps,
// policy outcomes (error / deny / pending / unknown), nil docker 503,
// insert-row 500 and the hub-broadcast happy path with a chat id.
// Helpers prefixed covPX.

// covPXPolicy is a configurable PortExposePolicy stub.
type covPXPolicy struct {
	decision PortExposeDecision
	reason   string
	err      error
}

func (p covPXPolicy) Check(_ context.Context, _ *PortExposeRequest) (PortExposeDecision, string, error) {
	return p.decision, p.reason, p.err
}

func covPXBody() map[string]any {
	return map[string]any{
		"workspace_id": "ws1",
		"crew_id":      "crew1",
		"agent_id":     "agent1",
		"container_id": "cont1",
		"port":         8080,
	}
}

func covPXHandler(t *testing.T, policy PortExposePolicy, docker DockerInspector, hub *ws.Hub) (*PortExposeHandler, *sql.DB) {
	t.Helper()
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	cfg := DefaultPortExposeConfig()
	cfg.PublicBaseURL = "http://covpx.local:8080"
	reg := NewPortExposeRegistry(db, newTestLogger())
	return NewPortExposeHandler(db, reg, docker, policy, hub, cfg, newTestLogger()), db
}

func TestCovPX_RequestExpose_InvalidJSON(t *testing.T) {
	h, _ := covPXHandler(t, AllowAllPolicy{}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/port-expose", strings.NewReader("{nope"))
	rr := httptest.NewRecorder()
	h.RequestExpose(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovPX_RequestExpose_DescriptionTooLong(t *testing.T) {
	h, _ := covPXHandler(t, AllowAllPolicy{}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	body := covPXBody()
	body["description"] = strings.Repeat("d", 201)
	rr := postJSON(t, h.RequestExpose, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "description too long") {
		t.Errorf("body = %s, want description too long", rr.Body.String())
	}
}

func TestCovPX_RequestExpose_BoundaryCheckDBError(t *testing.T) {
	h, db := covPXHandler(t, AllowAllPolicy{}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	db.Close()
	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovPX_RequestExpose_PolicyError(t *testing.T) {
	h, _ := covPXHandler(t, covPXPolicy{err: errors.New("policy backend down")}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovPX_RequestExpose_PolicyDeny(t *testing.T) {
	h, _ := covPXHandler(t, covPXPolicy{decision: ExposeDeny, reason: "nope"}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "denied by policy: nope") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestCovPX_RequestExpose_PolicyPending(t *testing.T) {
	h, _ := covPXHandler(t, covPXPolicy{decision: ExposePending}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rr.Code)
	}
}

func TestCovPX_RequestExpose_PolicyUnknownDecision(t *testing.T) {
	h, _ := covPXHandler(t, covPXPolicy{decision: PortExposeDecision("weird")}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovPX_RequestExpose_NilDocker503(t *testing.T) {
	h, _ := covPXHandler(t, AllowAllPolicy{}, nil, nil)
	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "container inspection not configured") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestCovPX_RequestExpose_InsertRowDBError(t *testing.T) {
	h, db := covPXHandler(t, AllowAllPolicy{}, &fakeDockerInspector{ip: "10.0.0.2"}, nil)
	// Keep the table readable (checkQuota must pass) but make INSERT fail.
	if _, err := db.Exec(`CREATE TRIGGER covpx_fail_insert BEFORE INSERT ON port_exposures BEGIN SELECT RAISE(ABORT, 'covpx boom'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovPX_RequestExpose_HappyWithChatIDAndHubAndTTLClamp(t *testing.T) {
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	h, db := covPXHandler(t, AllowAllPolicy{}, &fakeDockerInspector{ip: "10.0.0.9"}, hub)

	body := covPXBody()
	body["chat_id"] = "chat-77"
	body["description"] = "demo server"
	body["ttl_seconds"] = 99999999 // far above MaxTTL -> clamped down
	rr := postJSON(t, h.RequestExpose, body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp requestResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" || !strings.Contains(resp.URL, resp.Token) {
		t.Errorf("resp = %+v, want token embedded in url", resp)
	}
	// TTL clamp: expires_at must be <= now + MaxTTL (+ slack), i.e. the
	// 99999999s request did not survive.
	exp, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	max := time.Now().Add(DefaultPortExposeConfig().MaxTTL + time.Minute)
	if exp.After(max) {
		t.Errorf("expires_at = %v, want clamped below %v", exp, max)
	}

	// Row persisted with the chat id.
	var chatID string
	if err := db.QueryRow(`SELECT chat_id FROM port_exposures WHERE id = ?`, resp.ID).Scan(&chatID); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if chatID != "chat-77" {
		t.Errorf("chat_id = %q, want chat-77", chatID)
	}
}

func TestCovPX_RequestExpose_TinyTTLBumpedToOneSecond(t *testing.T) {
	db := newHandlerTestDB(t)
	planWorkspace(t, db, "ws1", "crew1", "agent1", "viktor")
	cfg := DefaultPortExposeConfig()
	cfg.PublicBaseURL = "http://covpx.local:8080"
	cfg.DefaultTTL = 10 * time.Millisecond // < 1s -> floor branch
	cfg.MaxTTL = 20 * time.Millisecond
	reg := NewPortExposeRegistry(db, newTestLogger())
	h := NewPortExposeHandler(db, reg, &fakeDockerInspector{ip: "10.0.0.2"}, AllowAllPolicy{}, nil, cfg, newTestLogger())

	rr := postJSON(t, h.RequestExpose, covPXBody())
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
}
