package backup

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

func TestServiceRestoreRejectsLegacyMissingPhysicalData(t *testing.T) {
	p := &ExtractedPayload{DBDump: &DBDump{Tables: map[string][]map[string]any{"crews": {{"id": "crew", "slug": "crew", "services_json": `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`}}}}}
	if _, err := p.prepareServiceRestorePlan(context.Background(), 0); err == nil {
		t.Fatal("database-only bundle accepted despite missing quota image")
	}
}

func TestServiceRestoreSelectsFreshHostGenerationAfterOwnerRemap(t *testing.T) {
	source := serviceSnapshot{CrewID: "original", Service: "database", Volume: "data", Generation: 3}
	row := map[string]any{"id": "restored", "services_json": `[{"name":"database","quota_enforced":true,"image":"pinned-image","volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`}
	plan := &serviceRestorePlan{items: []serviceImport{{source: source, crewRow: row}}}
	if err := plan.selectGenerations(); err != nil {
		t.Fatal(err)
	}
	target := plan.items[0].target
	if target.Crew != "restored" || target.Generation <= 0 || target.Generation == source.Generation || target.Service != source.Service || target.Volume != source.Volume {
		t.Fatalf("unsafe target: %+v", target)
	}
	specs, err := declaredServiceSnapshots(row["services_json"].(string), "restored", "crew")
	if err != nil || len(specs) != 1 || specs[0].Generation != target.Generation {
		t.Fatalf("target declaration not pinned: %+v %v", specs, err)
	}
}

func TestServiceMaintenanceStatusIsWorkspaceBoundAndKeepsUnknownFence(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	workspace, crew := seedCovWorkspace(t, db, "maintenance_scope")
	otherWorkspace, otherCrew := seedCovWorkspace(t, db, "maintenance_other")
	for _, id := range []string{crew, otherCrew} {
		if _, err := servicelifecycle.BeginBackupFence(ctx, db, id, "restore"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := ServiceMaintenanceStatus(ctx, db, workspace)
	if err != nil || len(rows) != 1 || rows[0].CrewID != crew || !rows[0].ProducerLive {
		t.Fatalf("workspace maintenance leaked/omitted: %+v %v", rows, err)
	}
	rows, err = ServiceMaintenanceStatus(ctx, db, otherWorkspace)
	if err != nil || len(rows) != 1 || rows[0].CrewID != otherCrew {
		t.Fatalf("other owner status: %+v %v", rows, err)
	}
	if _, err = db.Exec(`UPDATE service_backup_fences SET producer_until='2000-01-01T00:00:00Z' WHERE crew_id=?`, crew); err != nil {
		t.Fatal(err)
	}
	rows, err = ServiceMaintenanceStatus(ctx, db, workspace)
	if err != nil || len(rows) != 1 || rows[0].ProducerLive {
		t.Fatalf("crash maintenance silently cleared: %+v %v", rows, err)
	}
}
