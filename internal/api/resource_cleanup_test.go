package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestCrewDelete_CleanupPendingScope(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCrewRow(t, db, "cleanup-crew", ws, "Cleanup", "cleanup")
	h := NewCrewHandler(db, newTestLogger())
	h.containerCleanup = &resourcelifecycle.Controller{DB: db, InstanceID: "instance", Connect: func(context.Context) (resourcelifecycle.Runtime, error) {
		t.Fatal("DELETE must not run background reconciler inline")
		return nil, nil
	}, BootAt: time.Now()}
	rr := httptest.NewRecorder()
	h.Delete(rr, crewDeleteReq(user, ws, "cleanup-crew"))
	var response struct {
		Success bool                     `json:"success"`
		Cleanup resourcelifecycle.Status `json:"cleanup"`
		Sidecar json.RawMessage          `json:"sidecar_teardown"`
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Cleanup.Scope != "containers" || response.Cleanup.State != "pending" || response.Cleanup.Complete || len(response.Sidecar) == 0 {
		t.Fatalf("%+v", response)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM resource_cleanup_status WHERE crew_id='cleanup-crew'`).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("durable status %s %v", state, err)
	}
}

func TestResourceCleanupInstanceAdminWithoutMembershipSeesAllWorkspaces(t *testing.T) {
	f := lonelyAdmin(t)
	f.r.containerCleanup = &resourcelifecycle.Controller{DB: f.db, InstanceID: "cleanup-instance"}
	for _, ws := range []string{"ws-old", "ws-new"} {
		mustExec(t, f.db, `INSERT INTO resource_cleanup_status(instance_id,crew_id,workspace_id,state,complete) VALUES('cleanup-instance',?,?,'pending',1)`, "deleted-"+ws, ws)
	}
	read := func(token, path string, wantCount int) {
		t.Helper()
		rr := f.do(token, "GET", path, "")
		wantCode(t, rr, http.StatusOK, "cleanup observations")
		var body struct {
			Items []resourcelifecycle.Status `json:"items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != wantCount {
			t.Fatalf("cleanup observations = %s, want %d rows", rr.Body.String(), wantCount)
		}
		if token == f.wsAdmin && wantCount == 1 && body.Items[0].CrewID != "deleted-ws-old" {
			t.Fatal("workspace admin received another workspace's cleanup observation")
		}
	}
	read(f.boss, "/api/v1/admin/resource-cleanup", 2)
	read(f.wsAdmin, "/api/v1/admin/resource-cleanup?workspace_id=ws-old", 1)
	rr := f.do(f.wsAdmin, "GET", "/api/v1/admin/resource-cleanup", "")
	if rr.Code == http.StatusOK {
		t.Fatal("workspace admin read instance observations without a workspace")
	}
	mustExec(t, f.db, `UPDATE cli_tokens SET scopes='["crews:read"]' WHERE user_id='boss'`)
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/resource-cleanup", ""), http.StatusForbidden, "instance cleanup requires instance:admin token scope")
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/resource-cleanup?workspace_id=ws-new", ""), http.StatusForbidden, "workspace selection must not bypass instance token scope")
}
