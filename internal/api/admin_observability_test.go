package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/diskusage"
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

func TestAdminObservability_Health_InjectedDiskProvider(t *testing.T) {
	h := NewAdminObservabilityHandler(logging.New("info", "json", nil))
	// Inject fakes so the test never touches the real filesystem — the
	// point of the provider-function fields.
	h.dataDir = func() (string, error) { return "/fake/data", nil }
	h.diskUsage = func(path string) (diskusage.Stats, error) {
		return diskusage.Stats{Path: path, TotalBytes: 100, FreeBytes: 40, UsedBytes: 60, UsedPct: 60}, nil
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/health", nil)
	ctx := context.WithValue(r.Context(), ctxRole, "OWNER")
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Health(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Disk struct {
			Path    string  `json:"path"`
			UsedPct float64 `json:"used_pct"`
		} `json:"disk"`
		LogLevel struct {
			Level string `json:"level"`
		} `json:"log_level"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Disk.Path != "/fake/data" || resp.Disk.UsedPct != 60 {
		t.Errorf("disk = %+v, want injected fake", resp.Disk)
	}
	if resp.LogLevel.Level != "info" {
		t.Errorf("log_level.level = %q, want info", resp.LogLevel.Level)
	}
}

func TestAdminObservability_Health_SurfacesDataDirError(t *testing.T) {
	h := NewAdminObservabilityHandler(logging.New("info", "json", nil))
	h.dataDir = func() (string, error) { return "", errors.New("unresolvable data dir") }

	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/health", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, "OWNER"))
	w := httptest.NewRecorder()
	h.Health(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (health still reports what it can)", w.Code)
	}
	// The disk section must carry the error, not be silently omitted.
	if !strings.Contains(w.Body.String(), "unresolvable data dir") {
		t.Errorf("data-dir error not surfaced in disk section: %s", w.Body.String())
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
