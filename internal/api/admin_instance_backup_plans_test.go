package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backupplan"
)

func TestInstanceBackupPlansAreForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/instance/backups/plans", ""},
		{"POST", "/api/v1/admin/instance/backups/plans", `{"preset":"workspace"}`},
		{"POST", "/api/v1/admin/instance/backups/plans/preview-contents", `{"preset":"custom","contents":["memory"]}`},
		{"GET", "/api/v1/admin/instance/backups/runs", ""},
		{"POST", "/api/v1/admin/instance/backups/run", `{"scope":"workspaces"}`},
		{"GET", "/api/v1/admin/instance/backups/overview", ""},
	} {
		wantCode(t, f.do(f.wsAdmin, c.method, c.path, c.body), http.StatusForbidden, "workspace ADMIN "+c.method+" "+c.path)
	}
}

func ageKey(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Recipient().String()
}

func TestInstanceBackupPlansCRUD(t *testing.T) {
	f := newInstanceFixture(t)
	key := ageKey(t)

	// Validation: every plan is encrypted, and the zone is a real one.
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans", `{"preset":"workspace","workspace_ids":["ws-new"]}`)
	wantCode(t, rr, http.StatusBadRequest, "plan without recipients")
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans", `{"preset":"workspace","recipient_ids":["`+key+`"],"timezone":"Mars/Base"}`)
	wantCode(t, rr, http.StatusBadRequest, "bad timezone")
	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans", `{"preset":"workspace","recipient_ids":["`+key+`"],"cadence":"custom","cron_expr":"nope"}`)
	wantCode(t, rr, http.StatusBadRequest, "bad cron")

	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans",
		`{"preset":"workspace","name":"Nightly","workspace_ids":["ws-new"],"recipient_ids":["`+key+`"],"timezone":"Europe/Prague"}`)
	wantCode(t, rr, http.StatusCreated, "create")
	created := decodeAs[backupplan.Plan](t, rr.Body.Bytes())
	if created.ID == "" || created.Name != "Nightly" || created.Cadence != "daily" || created.TimeOfDay != "03:00" ||
		created.KeepMin != 3 || created.NextRunAt == nil || !created.CountsAsProtection || created.Scope != "workspaces" {
		t.Fatalf("created = %s", rr.Body.String())
	}
	if !f.audited("instance.backup_plan_created") {
		t.Fatal("create left no instance audit entry")
	}

	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/plans", "")
	wantCode(t, rr, http.StatusOK, "list")
	list := decodeAs[struct {
		Data []backupplan.Plan `json:"data"`
	}](t, rr.Body.Bytes())
	if len(list.Data) != 1 || list.Data[0].ID != created.ID {
		t.Fatalf("list = %s", rr.Body.String())
	}

	// PUT changes only what it sends.
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/plans/"+created.ID, `{"cadence":"weekly","weekday":0,"env_mode":"complete"}`)
	wantCode(t, rr, http.StatusOK, "update")
	updated := decodeAs[backupplan.Plan](t, rr.Body.Bytes())
	if updated.Cadence != "weekly" || updated.Name != "Nightly" || updated.Timezone != "Europe/Prague" || updated.EnvMode != "complete" {
		t.Fatalf("updated = %s", rr.Body.String())
	}
	if !f.audited("instance.backup_plan_updated") {
		t.Fatal("update left no instance audit entry")
	}

	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/plans/"+created.ID+"/next?n=3", "")
	wantCode(t, rr, http.StatusOK, "next")
	next := decodeAs[struct {
		Runs []backupplan.NextRun `json:"runs"`
	}](t, rr.Body.Bytes())
	if len(next.Runs) != 3 || !next.Runs[0].Environments {
		t.Fatalf("next = %s", rr.Body.String())
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/plans/"+created.ID+"/next?n=0", ""), http.StatusBadRequest, "n=0")

	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/plans/"+created.ID+"/calendar?from=2026-10-01&to=2026-10-31", "")
	wantCode(t, rr, http.StatusOK, "calendar")
	cal := decodeAs[struct {
		Days []backupplan.CalendarDay `json:"days"`
	}](t, rr.Body.Bytes())
	if len(cal.Days) != 31 {
		t.Fatalf("calendar days = %d", len(cal.Days))
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/plans/"+created.ID+"/calendar?from=bad", ""), http.StatusBadRequest, "bad from")

	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans/preview-contents", `{"preset":"custom","contents":["memory"],"env_mode":"files"}`)
	wantCode(t, rr, http.StatusOK, "preview")
	var preview struct {
		Included []string `json:"included"`
		Required []struct {
			Key     string   `json:"key"`
			Because []string `json:"because"`
		} `json:"required"`
		Excluded []string `json:"excluded"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Included) != 1 || preview.Included[0] != "memory" || len(preview.Required) != 1 || preview.Required[0].Key != "agents" || len(preview.Excluded) != 8 {
		t.Fatalf("preview = %s", rr.Body.String())
	}
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans/preview-contents", `{"preset":"custom","contents":["everything"]}`), http.StatusBadRequest, "unknown category")

	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/instance/backups/plans/"+created.ID, ""), http.StatusNoContent, "delete")
	if !f.audited("instance.backup_plan_deleted") {
		t.Fatal("delete left no instance audit entry")
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/plans/"+created.ID, ""), http.StatusNotFound, "get deleted")
	wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/backups/plans/nope", `{}`), http.StatusNotFound, "put unknown")
}

// A manual run answers at once with its id and runs in the backup service;
// the history then lists it with its phases, and the overview counts it.
func TestInstanceBackupRunAndOverview(t *testing.T) {
	f := newInstanceFixture(t)
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	key := ageKey(t)
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/backups/plans",
		`{"preset":"workspace","name":"Nightly","workspace_ids":["ws-new"],"recipient_ids":["`+key+`"]}`)
	wantCode(t, rr, http.StatusCreated, "create")
	plan := decodeAs[backupplan.Plan](t, rr.Body.Bytes())

	rr = f.do(f.boss, "POST", "/api/v1/admin/instance/backups/run", `{"plan_id":"`+plan.ID+`","note":"before upgrade"}`)
	wantCode(t, rr, http.StatusAccepted, "run")
	started := decodeAs[struct {
		ID    string `json:"id"`
		RunID string `json:"run_id"`
	}](t, rr.Body.Bytes())
	if started.ID == "" || started.ID != started.RunID {
		t.Fatalf("run response = %s", rr.Body.String())
	}
	f.r.BackupPlans().Wait()

	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/runs?scope=workspaces&ws=ws-new&limit=10", "")
	wantCode(t, rr, http.StatusOK, "runs")
	runs := decodeAs[struct {
		Data []backupplan.RunView `json:"data"`
	}](t, rr.Body.Bytes())
	if len(runs.Data) != 1 {
		t.Fatalf("runs = %s", rr.Body.String())
	}
	run := runs.Data[0]
	if run.ID != started.ID || run.Status != "done" || run.Trigger != "manual" || run.PlanName == nil || *run.PlanName != "Nightly" ||
		run.BundlePath == nil || run.ProofLevel != 1 || len(run.Phases) != 5 || run.Note == nil || *run.Note != "before upgrade" {
		t.Fatalf("run = %s", rr.Body.String())
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/runs?ws=nope", ""), http.StatusNotFound, "unknown ws")
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/instance/backups/runs?scope=galaxy", ""), http.StatusBadRequest, "bad scope")
	if !f.audited("instance.backup_run_started") {
		t.Fatal("manual run left no instance audit entry")
	}

	// A workspace without a crew gets no coverage row of its own (it is
	// summed up instead); this one has a crew.
	mustExec(t, f.db, `INSERT INTO crews (id, workspace_id, slug, name) VALUES ('crew-new', 'ws-new', 'crew-new', 'Crew')`)
	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/overview?scope=workspaces&ws=ws-new", "")
	wantCode(t, rr, http.StatusOK, "overview")
	ov := decodeAs[backupplan.OverviewResponse](t, rr.Body.Bytes())
	if ov.Status.Verdict != "checksum_only" || len(ov.Nights) != 14 || ov.Nights[13].Status != "ok" || len(ov.Workspaces) != 1 ||
		ov.Workspaces[0].Status != "ok" || ov.Space.BackupsBytes == 0 {
		t.Fatalf("overview = %s", rr.Body.String())
	}
	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/backups/overview", "")
	wantCode(t, rr, http.StatusOK, "instance overview")
	if !strings.Contains(rr.Body.String(), `"verdict":"none"`) {
		t.Fatalf("instance overview = %s", rr.Body.String())
	}
}
