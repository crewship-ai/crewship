package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestRunExecutions_PaginationOutputAndWorkspaceBoundary(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "execution_pipeline", "recipe")
	seedRunRow(t, db, ws, "execution_pipeline", "recipe", "execution_run", "completed")
	for i := 0; i < 102; i++ {
		if _, err := db.Exec(`INSERT INTO pipeline_step_executions (id,run_id,step_id,execution_path,attempt,kind,status,started_at,output) VALUES (?,?,?, ?,1,'script','completed',?,'private result')`, string(rune('a'+i)), "execution_run", "step", string(rune('a'+i)), time.Now().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	request := func(workspace, query string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/executions"+query, nil), user, workspace, "OWNER")
		req.SetPathValue("runId", "execution_run")
		rr := httptest.NewRecorder()
		h.RunExecutions(rr, req)
		return rr
	}
	rr := request(ws, "")
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	var page struct {
		Rows   []map[string]any `json:"rows"`
		Cursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 100 || page.Cursor == "" {
		t.Fatalf("bad page: %d %s", len(page.Rows), page.Cursor)
	}
	if _, present := page.Rows[0]["output"]; present {
		t.Fatal("list downloaded output")
	}
	rr = request(ws, "?after="+page.Cursor)
	json.Unmarshal(rr.Body.Bytes(), &page)
	if len(page.Rows) != 2 {
		t.Fatalf("second page: %s", rr.Body.String())
	}
	rr = request(ws, "?execution_id=a")
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	if request("foreign", "?execution_id=a").Code != 404 {
		t.Fatal("foreign workspace exposed output")
	}
	if request(ws, "?after=-1").Code != 400 {
		t.Fatal("invalid cursor accepted")
	}
}

func TestOneTimeAuthoring_AtomicAndNeverRearmsConsumedStart(t *testing.T) {
	_, db, user, ws := runsHandlerRig(t)
	store := pipeline.NewStore(db)
	now := time.Now()
	in := pipeline.SaveInput{WorkspaceID: ws, Slug: "one-time", Name: "One time", DefinitionJSON: `{"dsl_version":"1.0","name":"one-time","agentless":true,"steps":[{"id":"done","type":"transform","transform":{"expression":"."}}]}`, LastTestRunAt: &now, LastTestRunPassed: true, Author: pipeline.AuthorMeta{UserID: user, Via: pipeline.AuthoredViaUser}}
	trigger := &pipeline.TriggerInput{Kind: pipeline.TriggerKindOnce, FireAt: now.Add(time.Hour)}
	p, _, err := store.SaveWithTrigger(context.Background(), in, trigger)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.SaveWithTrigger(context.Background(), in, trigger); err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM pending_runs WHERE pipeline_id=?`, p.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("duplicate starts: %d", count)
	}
	db.Exec(`UPDATE pending_runs SET status='cancelled' WHERE pipeline_id=?`, p.ID)
	if _, _, err = store.SaveWithTrigger(context.Background(), in, trigger); err == nil {
		t.Fatal("rearmed consumed start")
	}
	in.Slug = "past-date"
	trigger.FireAt = now.Add(-time.Hour)
	if _, _, err = store.SaveWithTrigger(context.Background(), in, trigger); err == nil {
		t.Fatal("accepted past date")
	}
	db.QueryRow(`SELECT count(*) FROM pipelines WHERE slug='past-date'`).Scan(&count)
	if count != 0 {
		t.Fatal("failed trigger left partial routine")
	}
}

func TestRoutineCalendar_DoesNotInventPastOccurrences(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "calendar_pipeline", "calendar-recipe")
	now := time.Now().UTC()
	// A future one-time occurrence has no yearly repeating cron counterpart.
	_, _, err := pipeline.NewPendingRunStore(db).Enqueue(context.Background(), pipeline.PendingRun{ID: "one_date", WorkspaceID: ws, PipelineID: "calendar_pipeline", PipelineSlug: "calendar-recipe", FireAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/calendar?from="+now.Add(-24*time.Hour).Format(time.RFC3339)+"&to="+now.Add(24*time.Hour).Format(time.RFC3339), nil), user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.RoutineCalendar(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	var data struct {
		Events []map[string]any `json:"events"`
	}
	json.Unmarshal(rr.Body.Bytes(), &data)
	if len(data.Events) != 1 || data.Events[0]["kind"] != "pending" {
		t.Fatalf("unexpected occurrences: %s", rr.Body.String())
	}
}
