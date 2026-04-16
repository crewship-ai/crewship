package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newAuditHandler(t *testing.T) (*AuditHandler, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewAuditHandler(db, logger), db
}

func seedAuditLog(t *testing.T, db *sql.DB, wsID, userID, action, entityType string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO audit_logs (id, workspace_id, user_id, action, entity_type, entity_id, ip_address, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		generateCUID(), wsID, userID, action, entityType, "ent-1", "127.0.0.1", "go-test"); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
}

func TestAudit_List_Forbidden(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	ctx := withWorkspace(req.Context(), wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestAudit_List_AsMemberForbidden(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	ctx := withWorkspace(req.Context(), wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER status = %d, want 403 (manage required)", rr.Code)
	}
}

func TestAudit_List_Empty(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 0 {
		t.Errorf("data = %d, want 0", len(resp.Data))
	}
	if resp.Pagination.Page != 1 {
		t.Errorf("page = %d, want 1", resp.Pagination.Page)
	}
}

func TestAudit_List_Pagination(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	for i := 0; i < 5; i++ {
		seedAuditLog(t, db, wsID, userID, "user.login", "user")
	}

	req := httptest.NewRequest("GET", "/api/v1/audit?limit=2&page=1", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 2 {
		t.Errorf("data = %d, want 2 (limit)", len(resp.Data))
	}
	if resp.Pagination.Total != 5 {
		t.Errorf("total = %d, want 5", resp.Pagination.Total)
	}
	if resp.Pagination.TotalPages != 3 {
		t.Errorf("total_pages = %d, want 3", resp.Pagination.TotalPages)
	}
}

func TestAudit_List_FilterByAction(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditLog(t, db, wsID, userID, "user.login", "user")
	seedAuditLog(t, db, wsID, userID, "credential.create", "credential")

	req := httptest.NewRequest("GET", "/api/v1/audit?action=user.login", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Action != "user.login" {
		t.Errorf("filter result = %+v", resp.Data)
	}
}

func TestAudit_List_FilterByEntityType(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditLog(t, db, wsID, userID, "x", "user")
	seedAuditLog(t, db, wsID, userID, "y", "credential")

	req := httptest.NewRequest("GET", "/api/v1/audit?entity_type=credential", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].EntityType != "credential" {
		t.Errorf("filter result = %+v", resp.Data)
	}
}

func TestAudit_List_FilterByEntityID(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditLog(t, db, wsID, userID, "x", "user")

	req := httptest.NewRequest("GET", "/api/v1/audit?entity_id=ent-1", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestAudit_List_FilterByUserID(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditLog(t, db, wsID, userID, "x", "user")

	req := httptest.NewRequest("GET", "/api/v1/audit?user_id="+userID, nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestAudit_List_FilterByDateRange(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditLog(t, db, wsID, userID, "x", "user")

	req := httptest.NewRequest("GET", "/api/v1/audit?date_from=2020-01-01&date_to=2099-12-31", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Errorf("date filter data = %d, want 1", len(resp.Data))
	}
}

func TestAudit_List_FilterBySearch(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAuditLog(t, db, wsID, userID, "credential.create", "credential")

	req := httptest.NewRequest("GET", "/api/v1/audit?search=credential", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) == 0 {
		t.Error("expected matches for 'credential' search")
	}
}

func TestAudit_List_OnlyOwnWorkspace(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Insert log for OTHER workspace
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedAuditLog(t, db, "ws-other", userID, "x", "user")
	seedAuditLog(t, db, wsID, userID, "y", "user")

	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Action != "y" {
		t.Errorf("expected only own workspace's log, got %+v", resp.Data)
	}
}

func TestAudit_List_DefaultLimitClamped(t *testing.T) {
	t.Parallel()
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// limit > 100 should clamp to default 50
	req := httptest.NewRequest("GET", "/api/v1/audit?limit=99999", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp auditListResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Pagination.Limit != 50 {
		t.Errorf("limit = %d, want clamped to 50", resp.Pagination.Limit)
	}
}
