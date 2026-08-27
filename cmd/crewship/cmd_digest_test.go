package main

// #1422 item 4: `crewship digest enable` — ensures the workspace-digest
// routine exists (creating it from seeddata.WorkspaceDigestDefinition via
// the normal test_run -> save_token -> save flow if missing) and that a
// schedule fires it, idempotently.

import (
	"encoding/json"
	"io"
	"os"
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

// `digest enable --crew <slug> -f json` must emit ONLY the JSON document.
//
// The routine-creation path prints a server-side dry-run progress line, and it
// went straight to stdout — so on the one invocation that creates the routine,
// the machine output was a sentence followed by an object, and `jq` failed on
// the whole thing. That is the same defect this change removes everywhere
// else, surviving on the branch its own test for `digest enable` did not take:
// the other cases arrange a routine that already exists.
func TestDigestEnable_CreateUnderJSON_EmitsOnlyTheDocument(t *testing.T) {
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
	setFormatCov(t, "json")

	out := captureStdoutCovCli2(t, func() {
		if err := digestEnableCmd.RunE(digestEnableCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	// Decode the WHOLE of stdout. A substring search for `{` would call
	// "prose, then a valid object" a pass, which is exactly the bug.
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("digest enable -f json stdout is not a single JSON document: %v\ngot:\n%s", err, out)
	}
	if doc["routine_created"] != true {
		t.Errorf("routine_created = %v, want true (this is the create path)", doc["routine_created"])
	}
	// The progress line still has to exist for a person — it just belongs in
	// the human transcript, not in front of the document.
	if strings.Contains(out, "Validating workspace-digest") {
		t.Errorf("the dry-run progress line reached machine stdout:\n%s", out)
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

// captureDigestStreams runs fn with os.Stdout and os.Stderr redirected into
// SEPARATE pipes, because the whole question here is which stream a line
// landed on and how many times.
func captureDigestStreams(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	guardCLIState(t)
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-outCh, <-errCh
}

// `digest enable --when ... ` without --yes is the DEFAULT interactive path,
// and it printed every advisory line twice.
//
// The confirmation prompt needs the preview above it, so the collected notes
// are flushed to stderr before the question. That flush does not consume them,
// so the AutoHuman human renderer at the end replayed the whole slice —
// including the six lines already shown — to stdout. The pre-fix code printed
// each line exactly once; deferring the human output must not have changed
// how many times a person sees it.
func TestDigestEnable_When_Interactive_PrintsEachNoteOnce(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	routinePath := "/api/v1/workspaces/" + covWS + "/pipelines/workspace-digest"
	schedulesPath := "/api/v1/workspaces/" + covWS + "/pipeline-schedules"

	stub.OnGet(routinePath, clitest.JSONResponse(200, map[string]any{"slug": "workspace-digest"}))
	stub.OnGet(schedulesPath, clitest.JSONResponse(200, []scheduleRow{}))
	stub.OnPost(schedulesPath, clitest.JSONResponse(201, scheduleRow{ID: "sch_x", CronExpr: "0 9 * * 1"}))
	setStubCLI(t, stub.URL())
	covResetFlags(t, digestEnableCmd)
	covSetFlags(t, digestEnableCmd, map[string]string{"when": "every monday at 9am"})
	covWithStdin(t, "y\n")

	stdout, stderr := captureDigestStreams(t, func() {
		if err := digestEnableCmd.RunE(digestEnableCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	// The preview lines belong to the prompt: stderr once, stdout never.
	for _, preview := range []string{
		"Routine workspace-digest already exists.",
		`Parsed "every monday at 9am" as cron "0 9 * * 1"`,
		"Next 3 fire times:",
	} {
		if n := strings.Count(stderr, preview); n != 1 {
			t.Errorf("stderr contains %q %d times, want 1\nstderr:\n%s", preview, n, stderr)
		}
		if strings.Contains(stdout, preview) {
			t.Errorf("stdout replays the prompt preview %q — every note is printed twice\n"+
				"stdout:\n%s\nstderr:\n%s", preview, stdout, stderr)
		}
	}

	// Everything decided AFTER the prompt is the command's answer and belongs
	// on stdout — once, and not on stderr.
	for _, result := range []string{
		"Scheduled workspace-digest: 0 9 * * 1 UTC (id=sch_x)",
		"Configure delivery to Slack/email",
	} {
		if n := strings.Count(stdout, result); n != 1 {
			t.Errorf("stdout contains %q %d times, want 1\nstdout:\n%s", result, n, stdout)
		}
		if strings.Contains(stderr, result) {
			t.Errorf("stderr carries the post-prompt result %q\nstderr:\n%s", result, stderr)
		}
	}

	// The fire-time bullets are the bulk of the duplication and the easiest to
	// count: three of them, on stderr, and nowhere else.
	if n := strings.Count(stderr, "  - 20"); n != 3 {
		t.Errorf("stderr has %d fire-time lines, want 3\nstderr:\n%s", n, stderr)
	}
	if n := strings.Count(stdout, "  - 20"); n != 0 {
		t.Errorf("stdout replays %d fire-time lines\nstdout:\n%s", n, stdout)
	}
}

// --yes skips the prompt entirely, so nothing has been flushed to stderr and
// the human renderer owns the whole transcript on stdout. This is the half a
// "just drop the notes after the prompt" fix would silently delete.
func TestDigestEnable_When_Confirmed_KeepsFullTranscriptOnStdout(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	routinePath := "/api/v1/workspaces/" + covWS + "/pipelines/workspace-digest"
	schedulesPath := "/api/v1/workspaces/" + covWS + "/pipeline-schedules"

	stub.OnGet(routinePath, clitest.JSONResponse(200, map[string]any{"slug": "workspace-digest"}))
	stub.OnGet(schedulesPath, clitest.JSONResponse(200, []scheduleRow{}))
	stub.OnPost(schedulesPath, clitest.JSONResponse(201, scheduleRow{ID: "sch_y", CronExpr: "0 9 * * 1"}))
	setStubCLI(t, stub.URL())
	covResetFlags(t, digestEnableCmd)
	covSetFlags(t, digestEnableCmd, map[string]string{"when": "every monday at 9am", "yes": "true"})

	stdout, stderr := captureDigestStreams(t, func() {
		if err := digestEnableCmd.RunE(digestEnableCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{
		"Routine workspace-digest already exists.",
		`Parsed "every monday at 9am" as cron "0 9 * * 1"`,
		"Next 3 fire times:",
		"Scheduled workspace-digest: 0 9 * * 1 UTC (id=sch_y)",
	} {
		if n := strings.Count(stdout, want); n != 1 {
			t.Errorf("stdout contains %q %d times, want 1\nstdout:\n%s", want, n, stdout)
		}
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("--yes wrote to stderr:\n%s", stderr)
	}
}
