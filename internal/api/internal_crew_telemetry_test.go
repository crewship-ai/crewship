package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestWorkspaceCrewTelemetry_RealMetricsAndWorkspaceFence(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, crew := range []struct{ id, workspace, name, slug string }{
		{"crew-lab", wsID, "Ops", "ops"},
		{"crew-foreign", "ws-foreign", "Foreign", "foreign"},
	} {
		if crew.workspace == "ws-foreign" {
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES ('ws-foreign','Foreign','foreign')`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,created_at,updated_at) VALUES (?,?,?,?,?,?)`, crew.id, crew.workspace, crew.name, crew.slug, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,kind,created_at,updated_at) VALUES ('crew-setup',?,'Setup','_crewship-setup','setup',?,?)`, wsID, now, now); err != nil {
		t.Fatal(err)
	}
	lister := newFakeCrewContainerLister([]provider.CrewContainerInfo{{ID: "container-1", Name: "crew-ops", Kind: provider.CrewContainerKindCrew, State: "running"}}, nil)
	lister.stats["container-1"] = &provider.ContainerMetrics{CPUPercent: 13.26, MemoryUsed: 96 * bytesPerMiB}
	h := NewInternalHandler(db, "test", newTestLogger())
	h.SetContainer(lister)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/crews/telemetry?workspace_id="+wsID, nil)
	rec := httptest.NewRecorder()
	h.WorkspaceCrewTelemetry(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Crews []struct {
			Name       string `json:"name"`
			Available  bool   `json:"available"`
			Containers []struct {
				CPU    *float64 `json:"cpu_percent"`
				Memory *int     `json:"memory_mb"`
			} `json:"containers"`
		} `json:"crews"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Crews) != 1 || got.Crews[0].Name != "Ops" || !got.Crews[0].Available || len(got.Crews[0].Containers) != 1 {
		t.Fatalf("wrong workspace inventory: %+v", got)
	}
	if c := got.Crews[0].Containers[0]; c.CPU == nil || *c.CPU != 13.3 || c.Memory == nil || *c.Memory != 96 {
		t.Fatalf("wrong metrics: %+v", c)
	}
	if lister.lastCrewID != "crew-lab" {
		t.Errorf("queried foreign crew: %s", lister.lastCrewID)
	}
}
