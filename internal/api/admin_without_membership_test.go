package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// Part C: the whole Admin works for an instance administrator who belongs to
// no workspace. An admin route lets them leave the workspace out (instance
// data) or name any live workspace without being a member (that workspace's
// settings); everyone else keeps the workspace rules they had.

// lonelyAdmin is boss with every membership removed: a named instance admin
// who belongs nowhere.
func lonelyAdmin(t *testing.T) *instanceFixture {
	t.Helper()
	f := newInstanceFixture(t)
	mustExec(t, f.db, `UPDATE users SET instance_role = 'ADMIN' WHERE id = 'boss'`)
	mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id = 'boss'`)
	return f
}

func TestAnInstanceAdminWithNoWorkspaceReadsTheInstanceWideAdminPages(t *testing.T) {
	f := lonelyAdmin(t)
	for _, path := range []string{
		"/api/v1/admin/health",
		"/api/v1/admin/log-level",
		"/api/v1/admin/rate-limits",
		"/api/v1/admin/security-posture",
		"/api/v1/admin/legacy-resources",
		"/api/v1/admin/keeper/config",
		"/api/v1/system/keeper",
		"/api/v1/system/aux-status",
		"/api/v1/notification-providers",
	} {
		rr := f.do(f.boss, "GET", path, "")
		if rr.Code != http.StatusOK && rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d without a workspace: %s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAdminStatsWithoutAWorkspaceCountTheWholeInstance(t *testing.T) {
	f := lonelyAdmin(t)
	rr := f.do(f.boss, "GET", "/api/v1/admin/stats", "")
	wantCode(t, rr, http.StatusOK, "stats")
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if ws, _ := body["workspaces"].(float64); ws < 2 {
		t.Fatalf("stats = %s, want every workspace counted", rr.Body.String())
	}
}

func TestAnInstanceAdminEditsAWorkspaceTheyAreNotIn(t *testing.T) {
	f := lonelyAdmin(t)
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/keeper/governance?workspace_id=ws-new", ""), http.StatusOK, "read ws-new governance")
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/keeper/governance?workspace_id=ws-new", `{"enabled":true}`), http.StatusOK, "write ws-new governance")
	if s, _, _ := governance.Get(context.Background(), f.db, "ws-new"); !s.Enabled {
		t.Fatal("the write did not land")
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/memory/config?workspace_id=ws-new", ""), http.StatusOK, "read ws-new memory config")
	wantCode(t, f.do(f.boss, "PATCH", "/api/v1/admin/memory/config?workspace_id=ws-new", `{"versions_retention_days":45}`), http.StatusOK, "write ws-new memory config")
	// A workspace that does not exist is a 404, not a way to act on nothing.
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/keeper/governance?workspace_id=nope", ""), http.StatusNotFound, "unknown workspace")
	// A per-workspace page with no workspace says so.
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/keeper/governance", ""), http.StatusBadRequest, "governance without a workspace")
}

func TestWorkspaceAdminsKeepTheirWorkspaceRules(t *testing.T) {
	f := lonelyAdmin(t)
	// wsadmin is ADMIN of ws-old and not an instance admin.
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/health?workspace_id=ws-old", ""), http.StatusOK, "own workspace")
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/health", ""), http.StatusBadRequest, "no workspace")
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/keeper/governance?workspace_id=ws-new", ""), http.StatusForbidden, "someone else's workspace")
	wantCode(t, f.do(f.wsAdmin, "PUT", "/api/v1/admin/keeper/governance?workspace_id=ws-new", `{"enabled":true}`), http.StatusForbidden, "write someone else's")
}
