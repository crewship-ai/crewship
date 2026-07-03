package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

func obsReq(method, body, role string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/v1/admin/log-level", nil)
	} else {
		r = httptest.NewRequest(method, "/api/v1/admin/log-level", strings.NewReader(body))
	}
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, "ws1")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u1"})
	ctx = context.WithValue(ctx, ctxRole, role)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h := NewAdminObservabilityHandler(logging.New("info", "json", nil))
	switch method {
	case http.MethodPut:
		h.SetLogLevel(w, r)
	default:
		h.GetLogLevel(w, r)
	}
	return w
}

func TestAdminObservability_SetLogLevel_ChangesLiveLevel(t *testing.T) {
	defer logging.ResetLevel()

	w := obsReq(http.MethodPut, `{"level":"debug","ttl_seconds":900}`, "OWNER")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp struct {
		Level     string  `json:"level"`
		Baseline  string  `json:"baseline"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Level != "debug" {
		t.Errorf("level = %q, want debug", resp.Level)
	}
	if resp.Baseline != "info" {
		t.Errorf("baseline = %q, want info", resp.Baseline)
	}
	if resp.ExpiresAt == nil {
		t.Error("expires_at nil for a ttl'd override")
	}
	// The live controller must actually be at debug now.
	if cur, _, _ := logging.LevelState(); cur != "debug" {
		t.Errorf("live level = %q, want debug", cur)
	}
}

func TestAdminObservability_SetLogLevel_RejectsBadLevel(t *testing.T) {
	defer logging.ResetLevel()
	w := obsReq(http.MethodPut, `{"level":"verbose"}`, "OWNER")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown level", w.Code)
	}
	if cur, _, _ := logging.LevelState(); cur != "info" {
		t.Errorf("live level changed to %q on a rejected set", cur)
	}
}

func TestAdminObservability_LogLevel_RBAC(t *testing.T) {
	defer logging.ResetLevel()
	// MEMBER lacks "manage" — both read and write must be forbidden.
	if w := obsReq(http.MethodGet, "", "MEMBER"); w.Code != http.StatusForbidden {
		t.Errorf("GET as MEMBER = %d, want 403", w.Code)
	}
	if w := obsReq(http.MethodPut, `{"level":"debug"}`, "MEMBER"); w.Code != http.StatusForbidden {
		t.Errorf("PUT as MEMBER = %d, want 403", w.Code)
	}
	// ADMIN has "manage".
	if w := obsReq(http.MethodGet, "", "ADMIN"); w.Code != http.StatusOK {
		t.Errorf("GET as ADMIN = %d, want 200", w.Code)
	}
}
