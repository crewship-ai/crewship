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
