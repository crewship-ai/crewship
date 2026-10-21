package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestManualSchedulePreservesOperatorAndDistinctScheduleOrigin(t *testing.T) {
	f := newChatIssueFixture(t)
	store := pipeline.NewStore(f.h.db)
	now := time.Now()
	recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "manual-schedule-origin", Name: "Manual schedule", DefinitionJSON: `{"name":"Manual schedule","steps":[{"id":"value","type":"transform","transform":{"input":"ok","expression":"."}}]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, SourceRunID: f.run, SourceAgentID: f.agent, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	var headOrigin string
	if err = f.h.db.QueryRow(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID).Scan(&headOrigin); err != nil {
		t.Fatal(err)
	}
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, "schedule-specific-chat", "schedule-specific-run")
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, "schedule-specific-run", f.agent, f.workspace)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if origin.ID == headOrigin {
		t.Fatal("fixture does not distinguish schedule and recipe authority")
	}
	schedules := pipeline.NewScheduleStore(f.h.db)
	schedule, err := schedules.Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: f.workspace, Name: "Manual", TargetPipelineID: recipe.ID, CronExpr: "0 7 * * *", Timezone: "UTC", Enabled: true, ChatOriginID: origin.ID})
	if err != nil {
		t.Fatal(err)
	}
	const operator = "manual-schedule-operator"
	execOrFatal(t, f.h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'Operator')`, operator, operator+"@example.test")
	execOrFatal(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('manual-schedule-membership',?,?,'OWNER')`, f.workspace, operator)
	h := NewPipelineHandler(f.h.db, newTestLogger(), &stubRunner{output: "unused"}, nil)
	runs := pipeline.NewRunStore(f.h.db)
	h.SetRunStore(runs)
	h.SetScheduleStore(schedules)
	run := func() *httptest.ResponseRecorder {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest("POST", "/schedule/run", nil), operator, f.workspace, "OWNER")
		req.SetPathValue("scheduleId", schedule.ID)
		rr := httptest.NewRecorder()
		h.RunSchedule(rr, req)
		return rr
	}
	rr := run()
	if rr.Code != http.StatusOK {
		t.Fatalf("manual run: %d %s", rr.Code, rr.Body.String())
	}
	var result struct {
		RunID string `json:"run_id"`
	}
	if err = json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	record, err := runs.Get(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ChatOriginID != origin.ID || record.InvokingUserID != operator || record.InvocationAuthority != pipeline.RoutineBatchAuthority {
		t.Fatalf("manual run lost one authority: origin=%q operator=%q policy=%q", record.ChatOriginID, record.InvokingUserID, record.InvocationAuthority)
	}
	// An authorized operator cannot replace a revoked schedule source with the
	// recipe's authority or their own launch permission.
	execOrFatal(t, f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	rr = run()
	if rr.Code != http.StatusForbidden {
		t.Fatalf("revoked schedule source: %d %s", rr.Code, rr.Body.String())
	}
	var count int
	if err = f.h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id=?`, recipe.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("revoked source admitted work: %d %v", count, err)
	}
}
