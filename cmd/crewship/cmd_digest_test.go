package main

// #1422 item 4: `crewship digest enable` — ensures the workspace-digest
// routine exists (creating it from seeddata.WorkspaceDigestDefinition via
// the normal test_run -> save_token -> save flow if missing) and that a
// schedule fires it, idempotently.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestDigestEnable_RoutineMissing_RequiresCrew(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	routinePath := "/api/v1/workspaces/" + covWS + "/pipelines/workspace-digest"
	stub.OnGet(routinePath, clitest.ErrorResponse(404, "pipeline not found"))
	setStubCLI(t, stub.URL())
	covResetFlags(t, digestEnableCmd)

	err := digestEnableCmd.RunE(digestEnableCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew") {
		t.Fatalf("expected --crew required error, got %v", err)
	}
}

func TestDigestEnable_CreatesRoutineAndSchedule(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	routinePath := "/api/v1/workspaces/" + covWS + "/pipelines/workspace-digest"
	testRunPath := "/api/v1/workspaces/" + covWS + "/pipelines/test_run"
	savePath := "/api/v1/workspaces/" + covWS + "/pipelines/save"
	schedulesPath := "/api/v1/workspaces/" + covWS + "/pipeline-schedules"

	stub.OnGet(routinePath, clitest.ErrorResponse(404, "pipeline not found"))
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrew_ops", "slug": "ops"},
	}))
	stub.OnPost(testRunPath, clitest.JSONResponse(200, map[string]any{
		"status": "DRY_RUN_OK", "save_token": "tok123",
	}))
	stub.OnPost(savePath, clitest.JSONResponse(201, map[string]any{
		"slug": "workspace-digest", "id": "pln_digest",
	}))
	stub.OnGet(schedulesPath, clitest.JSONResponse(200, []scheduleRow{}))
	stub.OnPost(schedulesPath, clitest.JSONResponse(201, scheduleRow{
		ID: "sch_digest", TargetPipelineSlug: "workspace-digest", CronExpr: "0 8 * * *", Timezone: "UTC",
	}))
	setStubCLI(t, stub.URL())
	covResetFlags(t, digestEnableCmd)
	covSetFlags(t, digestEnableCmd, map[string]string{"crew": "ops"})

	out := captureStdoutCovCli2(t, func() {
		if err := digestEnableCmd.RunE(digestEnableCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Created routine: workspace-digest") {
		t.Errorf("missing routine-created message:\n%s", out)
	}
	if !strings.Contains(out, "Scheduled workspace-digest") {
		t.Errorf("missing schedule-created message:\n%s", out)
	}

	saveCalls := stub.CallsFor("POST", savePath)
	if len(saveCalls) != 1 {
		t.Fatalf("POST save calls = %d", len(saveCalls))
	}
	var saveBody map[string]any
	clitest.MustDecodeJSONBody(saveCalls[0].Body, &saveBody)
	if saveBody["author_crew_id"] != "ccrew_ops" {
		t.Errorf("author_crew_id = %v, want ccrew_ops", saveBody["author_crew_id"])
	}
	if saveBody["save_token"] != "tok123" {
		t.Errorf("save_token = %v, want tok123", saveBody["save_token"])
	}

	schedCalls := stub.CallsFor("POST", schedulesPath)
	if len(schedCalls) != 1 {
		t.Fatalf("POST schedule calls = %d", len(schedCalls))
	}
	var schedBody map[string]any
	clitest.MustDecodeJSONBody(schedCalls[0].Body, &schedBody)
	if schedBody["target_pipeline_slug"] != "workspace-digest" {
		t.Errorf("target_pipeline_slug = %v", schedBody["target_pipeline_slug"])
	}
	if schedBody["cron_expr"] != "0 8 * * *" {
		t.Errorf("cron_expr = %v, want default", schedBody["cron_expr"])
	}
}

func TestDigestEnable_Idempotent_RoutineAndScheduleAlreadyExist(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	routinePath := "/api/v1/workspaces/" + covWS + "/pipelines/workspace-digest"
	schedulesPath := "/api/v1/workspaces/" + covWS + "/pipeline-schedules"

	stub.OnGet(routinePath, clitest.JSONResponse(200, map[string]any{"slug": "workspace-digest"}))
	stub.OnGet(schedulesPath, clitest.JSONResponse(200, []scheduleRow{
		{ID: "sch_existing", TargetPipelineSlug: "workspace-digest", CronExpr: "0 9 * * *"},
	}))
	setStubCLI(t, stub.URL())
	covResetFlags(t, digestEnableCmd)

	out := captureStdoutCovCli2(t, func() {
		if err := digestEnableCmd.RunE(digestEnableCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "already exists") {
		t.Errorf("missing routine-exists message:\n%s", out)
	}
	if !strings.Contains(out, "already targets workspace-digest") {
		t.Errorf("missing schedule-exists message:\n%s", out)
	}
	if calls := stub.CallsFor("POST", schedulesPath); len(calls) != 0 {
		t.Errorf("expected no schedule POST when one already exists, got %d", len(calls))
	}
}

func TestDigestEnable_When_ParsesAndConfirms(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	routinePath := "/api/v1/workspaces/" + covWS + "/pipelines/workspace-digest"
	schedulesPath := "/api/v1/workspaces/" + covWS + "/pipeline-schedules"

	stub.OnGet(routinePath, clitest.JSONResponse(200, map[string]any{"slug": "workspace-digest"}))
	stub.OnGet(schedulesPath, clitest.JSONResponse(200, []scheduleRow{}))
	stub.OnPost(schedulesPath, clitest.JSONResponse(201, scheduleRow{ID: "sch_x", CronExpr: "0 9 * * *"}))
	setStubCLI(t, stub.URL())
	covResetFlags(t, digestEnableCmd)
	covSetFlags(t, digestEnableCmd, map[string]string{"when": "every day at 9am", "yes": "true"})

	out := captureStdoutCovCli2(t, func() {
		if err := digestEnableCmd.RunE(digestEnableCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `Parsed "every day at 9am" as cron "0 9 * * *"`) {
		t.Errorf("missing NL parse echo:\n%s", out)
	}
	calls := stub.CallsFor("POST", schedulesPath)
	if len(calls) != 1 {
		t.Fatalf("POST schedule calls = %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["cron_expr"] != "0 9 * * *" {
		t.Errorf("cron_expr = %v, want derived cron", body["cron_expr"])
	}
}

func TestDigestEnable_CronAndWhenMutuallyExclusive(t *testing.T) {
	covResetFlags(t, digestEnableCmd)
	covSetFlags(t, digestEnableCmd, map[string]string{"cron": "* * * * *", "when": "every hour"})
	err := digestEnableCmd.RunE(digestEnableCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("got %v", err)
	}
}
