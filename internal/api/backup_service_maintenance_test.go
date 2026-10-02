package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

func TestBackupServiceMaintenanceStatusKeepsWorkspaceAudience(t *testing.T) {
	h, user, workspace := backupRig(t)
	ownCrew := seedTestCrew(t, h.db, workspace)
	foreignWorkspace := "foreign-maintenance-workspace"
	if _, err := h.db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Foreign','foreign-maintenance')`, foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	foreignCrew := seedTestCrew(t, h.db, foreignWorkspace)
	for _, crew := range []string{ownCrew, foreignCrew} {
		if _, err := servicelifecycle.BeginBackupFence(t.Context(), h.db, crew, "restore"); err != nil {
			t.Fatal(err)
		}
	}
	request := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/admin/backups/status", nil), user, workspace, "OWNER")
	response := httptest.NewRecorder()
	h.Status(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var decoded backupStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Held || len(decoded.ServiceMaintenance) != 1 || decoded.ServiceMaintenance[0].CrewID != ownCrew || !decoded.ServiceMaintenance[0].ProducerLive {
		t.Fatalf("maintenance omitted or leaked: %+v", decoded)
	}
	if strings.Contains(response.Body.String(), foreignCrew) || strings.Contains(response.Body.String(), "token") || strings.Contains(response.Body.String(), "namespace") {
		t.Fatal("maintenance response disclosed foreign/runtime selectors")
	}
}
