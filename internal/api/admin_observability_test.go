package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	h := NewAdminObservabilityHandler(nil, logging.New("info", "json", nil))
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
	h := NewAdminObservabilityHandler(nil, logging.New("info", "json", nil))
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
	h := NewAdminObservabilityHandler(nil, logging.New("info", "json", nil))
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

// The overview health dots must reflect reality (#868): Health pings the DB
// and reports connected true on a live handle, false with an error once the
// handle is closed.
func TestAdminObservability_Health_DBProbe(t *testing.T) {
	db := setupTestDB(t)
	h := NewAdminObservabilityHandler(db, logging.New("info", "json", nil))

	call := func() map[string]any {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/health", nil)
		r = r.WithContext(context.WithValue(r.Context(), ctxRole, "OWNER"))
		w := httptest.NewRecorder()
		h.Health(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		dbSection, ok := resp["db"].(map[string]any)
		if !ok {
			t.Fatalf("db section missing/wrong type: %v", resp["db"])
		}
		return dbSection
	}

	if got := call(); got["connected"] != true {
		t.Errorf("live DB: connected = %v, want true", got["connected"])
	}

	_ = db.Close()
	if got := call(); got["connected"] != false {
		t.Errorf("closed DB: connected = %v, want false (body must show red)", got["connected"])
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

// A level change is in the instance audit — who, from what, to what, for how
// long — and so is the server putting it back when the time runs out.
func TestAdminObservability_SetLogLevel_IsAudited(t *testing.T) {
	defer logging.ResetLevel()
	defer logging.OnAutoRevert(nil)
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name, created_at, updated_at) VALUES ('u-ops', 'ops@example.com', 'Ops', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	h := NewAdminObservabilityHandler(db, logging.New("info", "json", nil))
	r := httptest.NewRequest(http.MethodPut, "/api/v1/admin/log-level", strings.NewReader(`{"level":"error","ttl_seconds":1}`))
	ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: "u-ops"})
	ctx = context.WithValue(ctx, ctxRole, "OWNER")
	w := httptest.NewRecorder()
	h.SetLogLevel(w, r.WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var user, meta string
	if err := db.QueryRow(`SELECT COALESCE(user_id, ''), metadata FROM instance_audit_logs WHERE action = 'instance.log_level_changed'`).Scan(&user, &meta); err != nil {
		t.Fatalf("no audit row: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		t.Fatal(err)
	}
	if user != "u-ops" || m["from"] != "info" || m["to"] != "error" || m["ttl_seconds"] != float64(1) || m["expires_at"] == nil {
		t.Errorf("audit = user %q %v, want u-ops info → error for 1 s with expires_at", user, m)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action = 'instance.log_level_reverted' AND user_id IS NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the revert at expiry was not audited")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
