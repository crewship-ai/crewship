package api

// Coverage tests for task_handler.go, admin_gdpr.go and the read-only
// backup query paths (backup_query.go + helpers in backup.go).
//
// Skipped intentionally:
//   - backup.go Create/Restore Docker snapshot + age-recipient flows and
//     the RestoreBackup runner: those need Docker or a DB-dump binary and
//     are exercised elsewhere (backup_mutation_test.go / backup pkg tests).
//     This file only touches the query/list/status read paths.
//
// Fault-injection convention for the 500 paths: seed valid rows, build a
// valid request, then db.Close() so the first query inside the handler
// errors → assert rr.Code == 500.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// ---------------------------------------------------------------------------
// Shared rigs / helpers (prefixed covTGB to avoid collisions)
// ---------------------------------------------------------------------------

// covTGBMissionRig spins up a MissionHandler over a freshly-migrated DB
// with a workspace, owner user, crew, lead agent and an IN_PROGRESS
// mission. Mirrors newMissionHandlerForTasks but lives here so this file
// is self-contained; the *sql.DB is reachable via h.db for the
// fault-injection (db.Close) tests.
func covTGBMissionRig(t *testing.T) (*MissionHandler, *coverMissionIDs) {
	t.Helper()
	db := setupTestDB(t)
	logger := newTestLogger()
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)

	missionID := generateCUID()
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`,
		missionID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Mission', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID, "trace-"+missionID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	return NewMissionHandler(db, nil, nil, logger), &coverMissionIDs{
		userID:    userID,
		wsID:      wsID,
		crewID:    crewID,
		leadID:    leadID,
		missionID: missionID,
	}
}

type coverMissionIDs struct {
	userID, wsID, crewID, leadID, missionID string
}

// covTGBInsertTask inserts a mission_tasks row with the supplied status
// and depends_on JSON, returning the task ID.
func covTGBInsertTask(t *testing.T, h *MissionHandler, missionID, status, dependsOn string) string {
	t.Helper()
	if dependsOn == "" {
		dependsOn = "[]"
	}
	id := generateCUID()
	if _, err := h.db.Exec(`INSERT INTO mission_tasks
		(id, mission_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES (?, ?, 'T', ?, 0, ?, datetime('now'), datetime('now'))`,
		id, missionID, status, dependsOn); err != nil {
		t.Fatalf("insert mission_task: %v", err)
	}
	return id
}

// covTGBTaskReq builds a request with crew/mission/task path values and
// OWNER workspace context.
func covTGBTaskReq(method, body string, ids *coverMissionIDs, taskID string) *http.Request {
	req := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	req.SetPathValue("crewId", ids.crewID)
	req.SetPathValue("missionId", ids.missionID)
	if taskID != "" {
		req.SetPathValue("taskId", taskID)
	}
	ctx := withUser(req.Context(), &AuthUser{ID: ids.userID})
	ctx = withWorkspace(ctx, ids.wsID, "OWNER")
	return req.WithContext(ctx)
}

// ---------------------------------------------------------------------------
// task_handler.go — CreateTask
// ---------------------------------------------------------------------------

func TestCovTGBCreateTask_WithDependencies_Blocked(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	// A pending dependency makes the new task BLOCKED.
	dep := covTGBInsertTask(t, h, ids.missionID, "PENDING", "")

	body := `{"title":"child","depends_on":["` + dep + `"]}`
	req := covTGBTaskReq("POST", body, ids, "")
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "BLOCKED" {
		t.Errorf("status = %q, want BLOCKED", resp.Status)
	}
}

func TestCovTGBCreateTask_WithCompletedDependency_Pending(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	dep := covTGBInsertTask(t, h, ids.missionID, "COMPLETED", "")

	body := `{"title":"child","depends_on":["` + dep + `"]}`
	req := covTGBTaskReq("POST", body, ids, "")
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "PENDING" {
		t.Errorf("status = %q, want PENDING", resp.Status)
	}
}

func TestCovTGBCreateTask_DependencyNotFound_400(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	body := `{"title":"child","depends_on":["does-not-exist"]}`
	req := covTGBTaskReq("POST", body, ids, "")
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovTGBCreateTask_DBClosed_500(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	h.db.Close() // fault injection: first query (mission lookup) errors
	req := covTGBTaskReq("POST", `{"title":"x"}`, ids, "")
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// task_handler.go — UpdateTask
// ---------------------------------------------------------------------------

func TestCovTGBUpdateTask_StatusTransition_Happy(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	taskID := covTGBInsertTask(t, h, ids.missionID, "PENDING", "")

	req := covTGBTaskReq("PATCH", `{"status":"IN_PROGRESS"}`, ids, taskID)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "IN_PROGRESS" {
		t.Errorf("status = %q, want IN_PROGRESS", resp.Status)
	}
}

func TestCovTGBUpdateTask_CompletedUnblocksDeps(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	parent := covTGBInsertTask(t, h, ids.missionID, "IN_PROGRESS", "")
	// child depends on parent and is BLOCKED until parent completes.
	covTGBInsertTask(t, h, ids.missionID, "BLOCKED", `["`+parent+`"]`)

	req := covTGBTaskReq("PATCH", `{"status":"COMPLETED"}`, ids, parent)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTGBUpdateTask_EditableFields_Happy(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	taskID := covTGBInsertTask(t, h, ids.missionID, "PENDING", "")

	req := covTGBTaskReq("PATCH", `{"title":"renamed","description":"d"}`, ids, taskID)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Title != "renamed" {
		t.Errorf("title = %q, want renamed", resp.Title)
	}
}

func TestCovTGBUpdateTask_EditAfterStarted_400(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	taskID := covTGBInsertTask(t, h, ids.missionID, "IN_PROGRESS", "")

	// title edit not allowed once the task left PENDING/BLOCKED.
	req := covTGBTaskReq("PATCH", `{"title":"nope"}`, ids, taskID)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovTGBUpdateTask_DependsOnRecalc_Blocked(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	dep := covTGBInsertTask(t, h, ids.missionID, "PENDING", "")
	taskID := covTGBInsertTask(t, h, ids.missionID, "PENDING", "")

	body := `{"depends_on":"[\"` + dep + `\"]"}`
	req := covTGBTaskReq("PATCH", body, ids, taskID)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "BLOCKED" {
		t.Errorf("status = %q, want BLOCKED", resp.Status)
	}
}

func TestCovTGBUpdateTask_MetadataFields_Happy(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	taskID := covTGBInsertTask(t, h, ids.missionID, "IN_PROGRESS", "")

	body := `{"result_summary":"done","token_count":42,"estimated_cost":1.5}`
	req := covTGBTaskReq("PATCH", body, ids, taskID)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTGBUpdateTask_StatusAndDependsOnConflict_400(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	taskID := covTGBInsertTask(t, h, ids.missionID, "PENDING", "")

	req := covTGBTaskReq("PATCH", `{"status":"IN_PROGRESS","depends_on":"[]"}`, ids, taskID)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovTGBUpdateTask_NotFound_404(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	req := covTGBTaskReq("PATCH", `{"status":"IN_PROGRESS"}`, ids, "missing-task")
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovTGBUpdateTask_DBClosed_500(t *testing.T) {
	h, ids := covTGBMissionRig(t)
	taskID := covTGBInsertTask(t, h, ids.missionID, "PENDING", "")
	h.db.Close() // fault injection: BeginTx fails
	req := covTGBTaskReq("PATCH", `{"status":"IN_PROGRESS"}`, ids, taskID)
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// admin_gdpr.go — ExportUserData / DeleteUserData
// ---------------------------------------------------------------------------

// covTGBGDPRRig builds an AdminGDPRHandler over a migrated DB with a
// workspace, admin user and a target data subject. Returns the handle so
// fault-injection tests can Close() it.
func covTGBGDPRRig(t *testing.T) (*AdminGDPRHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := newTestLogger()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('cov-ws','W','cov-w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('cov-admin','a@x'),('cov-subj','s@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	dir := t.TempDir()
	return NewAdminGDPRHandler(db, logger, dir), "cov-ws", "cov-subj"
}

func covTGBGDPRReq(method, body, userIDPath, wsID, actorID, role string) *http.Request {
	req := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	req.SetPathValue("userId", userIDPath)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: actorID})
	ctx = context.WithValue(ctx, ctxRole, role)
	return req.WithContext(ctx)
}

func TestCovTGBGDPRExport_MemberRole_403(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	req := covTGBGDPRReq("GET", "", subj, wsID, "cov-admin", "MEMBER")
	rr := httptest.NewRecorder()
	h.ExportUserData(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovTGBGDPRExport_NoUser_401(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	// No ctxUser set → adminContext returns 401.
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("userId", subj)
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "OWNER")
	rr := httptest.NewRecorder()
	h.ExportUserData(rr, req.WithContext(ctx))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovTGBGDPRExport_NoWorkspace_400(t *testing.T) {
	h, _, subj := covTGBGDPRRig(t)
	req := covTGBGDPRReq("GET", "", subj, "", "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.ExportUserData(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovTGBGDPRExport_MissingUserID_400(t *testing.T) {
	h, wsID, _ := covTGBGDPRRig(t)
	req := covTGBGDPRReq("GET", "", "", wsID, "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.ExportUserData(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovTGBGDPRExport_Happy_200(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	// Seed one row in each cascadable table referencing the subject.
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, slug, name, agent_role)
		VALUES ('cov-a','cov-ws','alice','Alice','AGENT')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO peer_cards
		(id, workspace_id, agent_id, user_id, user_slug, path, bytes)
		VALUES ('cov-pc','cov-ws','cov-a',?, 'slug','/p/slug.md',10)`, subj); err != nil {
		t.Fatalf("seed peer_cards: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, data_subject_id)
		VALUES ('cov-mv','cov-ws','/p','peer','sha',5,'/blob/sha',?)`, subj); err != nil {
		t.Fatalf("seed memory_versions: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title, data_subject_id)
		VALUES ('cov-ib','cov-ws','message','msg','t',?)`, subj); err != nil {
		t.Fatalf("seed inbox_items: %v", err)
	}

	req := covTGBGDPRReq("GET", "", subj, wsID, "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.ExportUserData(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var bundle gdprExportBundle
	if err := json.Unmarshal(rr.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(bundle.PeerCards) != 1 || len(bundle.MemoryVersion) != 1 || len(bundle.InboxItems) != 1 {
		t.Errorf("bundle counts pc=%d mv=%d ib=%d, want 1/1/1",
			len(bundle.PeerCards), len(bundle.MemoryVersion), len(bundle.InboxItems))
	}
}

func TestCovTGBGDPRExport_DBClosed_500(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	h.db.Close() // insertGDPRAction audit row fails → 500
	req := covTGBGDPRReq("GET", "", subj, wsID, "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.ExportUserData(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovTGBGDPRDelete_Happy_202(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	if _, err := h.db.Exec(`INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, data_subject_id)
		VALUES ('cov-mv2','cov-ws','/p','peer','sha2',5,'/blob/sha2',?)`, subj); err != nil {
		t.Fatalf("seed memory_versions: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title, data_subject_id)
		VALUES ('cov-ib2','cov-ws','message','msg','t',?)`, subj); err != nil {
		t.Fatalf("seed inbox_items: %v", err)
	}

	req := covTGBGDPRReq("DELETE", `{"reason":"SAR ticket #1"}`, subj, wsID, "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.DeleteUserData(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// rows should be gone afterwards.
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE data_subject_id = ?`, subj).Scan(&n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 0 {
		t.Errorf("memory_versions remaining = %d, want 0", n)
	}
}

func TestCovTGBGDPRDelete_MissingReason_400(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	req := covTGBGDPRReq("DELETE", `{}`, subj, wsID, "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.DeleteUserData(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovTGBGDPRDelete_BadJSON_400(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	req := covTGBGDPRReq("DELETE", `not json`, subj, wsID, "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.DeleteUserData(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovTGBGDPRDelete_MemberRole_403(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	req := covTGBGDPRReq("DELETE", `{"reason":"x"}`, subj, wsID, "cov-admin", "MEMBER")
	rr := httptest.NewRecorder()
	h.DeleteUserData(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovTGBGDPRDelete_DBClosed_500(t *testing.T) {
	h, wsID, subj := covTGBGDPRRig(t)
	h.db.Close() // insertGDPRAction audit row fails → 500
	req := covTGBGDPRReq("DELETE", `{"reason":"x"}`, subj, wsID, "cov-admin", "OWNER")
	rr := httptest.NewRecorder()
	h.DeleteUserData(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// backup_query.go / backup.go — read paths only (List / Status / Metrics)
// ---------------------------------------------------------------------------

func covTGBBackupRig(t *testing.T) (*BackupHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewBackupHandler(db, newTestLogger(), nil, "cov-version"), userID, wsID
}

func TestCovTGBBackupList_NonEmptyCatalog_200(t *testing.T) {
	h, _, wsID := covTGBBackupRig(t)

	// A real file on disk so ReconcileCatalog's Stat keeps the row
	// (a missing file would be pruned and the list would fall through
	// to the empty filesystem path). The List handler returns catalog
	// rows directly without a DefaultBackupsDir prefix check.
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "crewship-workspace-cov-w-20260101T000000Z.tar.zst")
	if err := os.WriteFile(bundlePath, []byte("stub"), 0o600); err != nil {
		t.Fatalf("write stub bundle: %v", err)
	}
	if err := backup.UpsertCatalogEntry(context.Background(), h.db, backup.CatalogEntry{
		ID:            "cov-cat-1",
		FilePath:      bundlePath,
		Scope:         "workspace",
		ScopeLevel:    "standard",
		Slug:          "cov-w",
		WorkspaceID:   wsID,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "admin@x",
		Size:          4,
		SHA256:        "deadbeef",
		Encrypted:     false,
		FormatVersion: 1,
	}); err != nil {
		t.Fatalf("upsert catalog: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), "u", wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data []struct {
			Path     string `json:"path"`
			FileName string `json:"file_name"`
			Scope    string `json:"scope"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Path != bundlePath || resp.Data[0].Scope != "workspace" {
		t.Errorf("entry = %+v, want path=%s scope=workspace", resp.Data[0], bundlePath)
	}
}

func TestCovTGBBackupList_DBClosed_500(t *testing.T) {
	h, _, wsID := covTGBBackupRig(t)
	h.db.Close() // ReconcileCatalog/ListCatalog query errors → 500
	req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), "u", wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovTGBBackupStatus_DBClosed_500(t *testing.T) {
	h, _, wsID := covTGBBackupRig(t)
	h.db.Close() // IsLockHeld query errors → 500
	req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), "u", wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovTGBBackupMetrics_NotInstanceOwner_403(t *testing.T) {
	h, userID, wsID := covTGBBackupRig(t)
	t.Setenv("CREWSHIP_OWNER_EMAIL", "owner@instance")
	req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovTGBBackupMetrics_NotAuthenticated_401(t *testing.T) {
	h, _, wsID := covTGBBackupRig(t)
	// No user in context → 401.
	ctx := withWorkspace(httptest.NewRequest("GET", "/", nil).Context(), wsID, "OWNER")
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestCovTGBBackupMetrics_InstanceOwner_200(t *testing.T) {
	h, _, wsID := covTGBBackupRig(t)
	t.Setenv("CREWSHIP_OWNER_EMAIL", "owner@instance")
	ctx := withUser(httptest.NewRequest("GET", "/", nil).Context(),
		&AuthUser{ID: "u", Email: "owner@instance"})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Metrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}
