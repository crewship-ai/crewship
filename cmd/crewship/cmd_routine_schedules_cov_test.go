package main

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const schedulesPath = "/api/v1/workspaces/" + covWS + "/pipeline-schedules"

func TestFormatTimestampCov(t *testing.T) {
	t.Parallel()
	// Unparseable input passes through verbatim.
	if got := formatTimestamp("not-a-time"); got != "not-a-time" {
		t.Errorf("passthrough: got %q", got)
	}
	// Valid RFC3339 renders in the host's local zone with the documented layout.
	iso := "2026-06-01T12:30:00Z"
	parsed, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.Local().Format("2006-01-02 15:04 MST")
	if got := formatTimestamp(iso); got != want {
		t.Errorf("formatTimestamp(%q) = %q, want %q", iso, got, want)
	}
}

func TestShortIDCov(t *testing.T) {
	t.Parallel()
	if got := shortID("short-id"); got != "short-id" {
		t.Errorf("short input must pass through, got %q", got)
	}
	if got := shortID("abcdefghijklmnop"); got != "abcdefghijklmnop" {
		t.Errorf("16 chars must pass through, got %q", got)
	}
	if got := shortID("abcdefghijklmnopq"); got != "abcdefghijklmn…" {
		t.Errorf("long id: got %q", got)
	}
}

func TestSetScheduleEnabled_Guards(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := setScheduleEnabled("sch1", true); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("no auth: got %v", err)
	}
	cliCfg = &cli.CLIConfig{Token: "tok"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	if err := setScheduleEnabled("sch1", true); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("no workspace: got %v", err)
	}
}

func TestScheduleEnableDisable_PatchBody(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPatch(schedulesPath+"/sch1", clitest.JSONResponse(200, map[string]string{"id": "sch1"}))
	setStubCLI(t, stub.URL())

	_ = captureStdoutCovCli2(t, func() {
		if err := routineSchedulesEnableCmd.RunE(routineSchedulesEnableCmd, []string{"sch1"}); err != nil {
			t.Errorf("enable: %v", err)
		}
		if err := routineSchedulesDisableCmd.RunE(routineSchedulesDisableCmd, []string{"sch1"}); err != nil {
			t.Errorf("disable: %v", err)
		}
	})

	calls := stub.CallsFor("PATCH", schedulesPath+"/sch1")
	if len(calls) != 2 {
		t.Fatalf("PATCH calls = %d, want 2", len(calls))
	}
	var first, second map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &first)
	clitest.MustDecodeJSONBody(calls[1].Body, &second)
	if first["enabled"] != true {
		t.Errorf("enable body = %v", first)
	}
	if second["enabled"] != false {
		t.Errorf("disable body = %v", second)
	}

	// API error surfaces.
	stub.OnPatch(schedulesPath+"/sch1", clitest.ErrorResponse(404, "schedule not found"))
	if err := setScheduleEnabled("sch1", true); err == nil || !strings.Contains(err.Error(), "schedule not found") {
		t.Errorf("API error: got %v", err)
	}
}

// newScheduleFlagsCmd mirrors the create/update/list flag sets on an
// isolated command so flag state never sticks to the global vars.
func newScheduleFlagsCmd() *cobra.Command {
	c := &cobra.Command{Use: "t"}
	c.Flags().String("slug", "", "")
	c.Flags().String("name", "", "")
	c.Flags().String("cron", "", "")
	c.Flags().String("timezone", "", "")
	c.Flags().String("inputs", "", "")
	c.Flags().Bool("enabled", true, "")
	c.Flags().String("wake-slug", "", "")
	c.Flags().String("wake-inputs", "", "")
	c.Flags().Bool("no-wake", false, "")
	c.Flags().Bool("json", false, "")
	c.Flags().Bool("yes", false, "")
	return c
}

func TestScheduleList_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setStubCLI(t, stub.URL())

	// Empty workspace → hint message.
	stub.OnGet(schedulesPath, clitest.JSONResponse(200, []scheduleRow{}))
	c := newScheduleFlagsCmd()
	out := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesListCmd.RunE(c, nil); err != nil {
			t.Errorf("empty list: %v", err)
		}
	})
	if !strings.Contains(out, "No schedules in this workspace.") {
		t.Errorf("empty message missing:\n%s", out)
	}

	next := "2026-06-12T09:00:00Z"
	rows := []scheduleRow{
		{
			ID: "cschedule00000000000x1y2", Name: "daily", TargetPipelineSlug: "summarize",
			CronExpr: "0 9 * * *", Timezone: "UTC", Enabled: true, NextRunAt: &next,
			WakePipelineSlug: "cost-probe", WakeCheckCount: 96, WakeFireCount: 3,
		},
		{
			ID: "sch2", Name: "weekly", TargetPipelineSlug: "report",
			CronExpr: "0 8 * * 1", Timezone: "Europe/Prague", Enabled: false,
		},
	}
	stub.OnGet(schedulesPath, clitest.JSONResponse(200, rows))

	// Table mode renders both rows with wake telemetry.
	c2 := newScheduleFlagsCmd()
	out2 := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesListCmd.RunE(c2, nil); err != nil {
			t.Errorf("table list: %v", err)
		}
	})
	for _, want := range []string{"daily", "summarize", "cost-probe 3/96", "weekly", "no", "—"} {
		if !strings.Contains(out2, want) {
			t.Errorf("table missing %q:\n%s", want, out2)
		}
	}
	if !strings.Contains(out2, "cschedule00000…") {
		t.Errorf("long id not shortened:\n%s", out2)
	}

	// Slug filter keeps only matching schedules.
	c3 := newScheduleFlagsCmd()
	_ = c3.Flags().Set("slug", "report")
	out3 := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesListCmd.RunE(c3, nil); err != nil {
			t.Errorf("filtered list: %v", err)
		}
	})
	if strings.Contains(out3, "summarize") || !strings.Contains(out3, "report") {
		t.Errorf("slug filter wrong:\n%s", out3)
	}

	// Filter that matches nothing → per-slug empty message.
	c4 := newScheduleFlagsCmd()
	_ = c4.Flags().Set("slug", "ghost")
	out4 := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesListCmd.RunE(c4, nil); err != nil {
			t.Errorf("ghost filter: %v", err)
		}
	})
	if !strings.Contains(out4, `No schedules for routine "ghost".`) {
		t.Errorf("ghost message missing:\n%s", out4)
	}

	// JSON mode emits the raw rows.
	c5 := newScheduleFlagsCmd()
	_ = c5.Flags().Set("json", "true")
	out5 := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesListCmd.RunE(c5, nil); err != nil {
			t.Errorf("json list: %v", err)
		}
	})
	if !strings.Contains(out5, `"cron_expr": "0 9 * * *"`) {
		t.Errorf("json output missing cron:\n%s", out5)
	}
}

func TestScheduleCreate_Validation(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "tok", Workspace: covWS}

	cases := []struct {
		name    string
		set     map[string]string
		wantErr string
	}{
		{"missing slug+cron", map[string]string{}, "--slug and --cron are required"},
		{"missing cron", map[string]string{"slug": "x"}, "--slug and --cron are required"},
		{"wake-inputs without wake-slug", map[string]string{"slug": "x", "cron": "* * * * *", "wake-inputs": `{"a":1}`}, "--wake-inputs requires --wake-slug"},
		{"bad inputs json", map[string]string{"slug": "x", "cron": "* * * * *", "inputs": "{nope"}, "--inputs must be valid JSON"},
		{"bad wake-inputs json", map[string]string{"slug": "x", "cron": "* * * * *", "wake-slug": "probe", "wake-inputs": "{nope"}, "--wake-inputs must be valid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newScheduleFlagsCmd()
			for k, v := range tc.set {
				if err := c.Flags().Set(k, v); err != nil {
					t.Fatal(err)
				}
			}
			err := routineSchedulesCreateCmd.RunE(c, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestScheduleCreate_Success(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	next := "2026-06-12T09:00:00Z"
	stub.OnPost(schedulesPath, clitest.JSONResponse(201, scheduleRow{
		ID: "sch-new", Name: "summarize schedule", CronExpr: "0 9 * * *",
		Timezone: "UTC", WakePipelineSlug: "probe", NextRunAt: &next,
	}))
	setStubCLI(t, stub.URL())

	c := newScheduleFlagsCmd()
	for k, v := range map[string]string{
		"slug": "summarize", "cron": "0 9 * * *",
		"inputs": `{"text":"hello"}`, "wake-slug": "probe", "wake-inputs": `{"limit":5}`,
	} {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesCreateCmd.RunE(c, nil); err != nil {
			t.Errorf("create: %v", err)
		}
	})
	for _, want := range []string{"Schedule created: summarize schedule", "ID:     sch-new", "Wake:   probe", "Next:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	calls := stub.CallsFor("POST", schedulesPath)
	if len(calls) != 1 {
		t.Fatalf("POST calls = %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["target_pipeline_slug"] != "summarize" || body["cron_expr"] != "0 9 * * *" {
		t.Errorf("body = %v", body)
	}
	// name defaults to "<slug> schedule"
	if body["name"] != "summarize schedule" {
		t.Errorf("default name = %v", body["name"])
	}
	if body["wake_pipeline_slug"] != "probe" {
		t.Errorf("wake slug = %v", body["wake_pipeline_slug"])
	}
	wakeInputs, _ := body["wake_inputs"].(map[string]any)
	if wakeInputs["limit"] != float64(5) {
		t.Errorf("wake inputs = %v", body["wake_inputs"])
	}
	inputs, _ := body["inputs"].(map[string]any)
	if inputs["text"] != "hello" {
		t.Errorf("inputs = %v", body["inputs"])
	}
}

func TestScheduleUpdate_ValidationAndBody(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPatch(schedulesPath+"/sch1", clitest.JSONResponse(200, map[string]string{"id": "sch1"}))
	setStubCLI(t, stub.URL())

	// No fields at all.
	c := newScheduleFlagsCmd()
	if err := routineSchedulesUpdateCmd.RunE(c, []string{"sch1"}); err == nil ||
		!strings.Contains(err.Error(), "at least one of") {
		t.Errorf("no fields: got %v", err)
	}

	// Mutually exclusive combinations.
	c2 := newScheduleFlagsCmd()
	_ = c2.Flags().Set("no-wake", "true")
	_ = c2.Flags().Set("wake-slug", "probe")
	if err := routineSchedulesUpdateCmd.RunE(c2, []string{"sch1"}); err == nil ||
		!strings.Contains(err.Error(), "--no-wake and --wake-slug are mutually exclusive") {
		t.Errorf("no-wake+wake-slug: got %v", err)
	}
	c3 := newScheduleFlagsCmd()
	_ = c3.Flags().Set("no-wake", "true")
	_ = c3.Flags().Set("wake-inputs", `{"a":1}`)
	if err := routineSchedulesUpdateCmd.RunE(c3, []string{"sch1"}); err == nil ||
		!strings.Contains(err.Error(), "--no-wake and --wake-inputs are mutually exclusive") {
		t.Errorf("no-wake+wake-inputs: got %v", err)
	}

	// Invalid JSON payloads.
	c4 := newScheduleFlagsCmd()
	_ = c4.Flags().Set("inputs", "{nope")
	if err := routineSchedulesUpdateCmd.RunE(c4, []string{"sch1"}); err == nil ||
		!strings.Contains(err.Error(), "--inputs must be valid JSON") {
		t.Errorf("bad inputs: got %v", err)
	}
	c5 := newScheduleFlagsCmd()
	_ = c5.Flags().Set("wake-slug", "probe")
	_ = c5.Flags().Set("wake-inputs", "{nope")
	if err := routineSchedulesUpdateCmd.RunE(c5, []string{"sch1"}); err == nil ||
		!strings.Contains(err.Error(), "--wake-inputs must be valid JSON") {
		t.Errorf("bad wake inputs: got %v", err)
	}

	// Happy path: cron + explicit enabled=false + no-wake clear.
	c6 := newScheduleFlagsCmd()
	_ = c6.Flags().Set("cron", "0 8 * * *")
	_ = c6.Flags().Set("enabled", "false")
	_ = c6.Flags().Set("no-wake", "true")
	_ = c6.Flags().Set("name", "renamed")
	_ = c6.Flags().Set("timezone", "Europe/Prague")
	_ = c6.Flags().Set("inputs", `{"k":"v"}`)
	out := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesUpdateCmd.RunE(c6, []string{"sch1"}); err != nil {
			t.Errorf("update: %v", err)
		}
	})
	if !strings.Contains(out, "Schedule sch1 updated.") {
		t.Errorf("output:\n%s", out)
	}
	calls := stub.CallsFor("PATCH", schedulesPath+"/sch1")
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["cron_expr"] != "0 8 * * *" || body["enabled"] != false ||
		body["wake_pipeline_slug"] != "" || body["name"] != "renamed" || body["timezone"] != "Europe/Prague" {
		t.Errorf("body = %v", body)
	}
}

func TestScheduleNow_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setStubCLI(t, stub.URL())

	// 404 → dedicated capability-missing error (the stub's fallback 404s).
	err := routineSchedulesNowCmd.RunE(routineSchedulesNowCmd, []string{"sch1"})
	if err == nil || !strings.Contains(err.Error(), "force-fire endpoint unavailable") {
		t.Errorf("404 path: got %v", err)
	}

	stub.OnPost(schedulesPath+"/sch1/run", clitest.JSONResponse(200, map[string]string{"run_id": "r1"}))
	out := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesNowCmd.RunE(routineSchedulesNowCmd, []string{"sch1"}); err != nil {
			t.Errorf("now: %v", err)
		}
	})
	if !strings.Contains(out, "Schedule sch1 fired (out-of-cycle).") {
		t.Errorf("output:\n%s", out)
	}

	// Non-404 API error surfaces via CheckError.
	stub.OnPost(schedulesPath+"/sch1/run", clitest.ErrorResponse(500, "scheduler offline"))
	err = routineSchedulesNowCmd.RunE(routineSchedulesNowCmd, []string{"sch1"})
	if err == nil || !strings.Contains(err.Error(), "scheduler offline") {
		t.Errorf("500 path: got %v", err)
	}
}

func TestScheduleDelete_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnDelete(schedulesPath+"/sch1", clitest.EmptyResponse(204))
	setStubCLI(t, stub.URL())

	// Without --yes on non-TTY stdin the confirm read hits EOF → aborted,
	// and nothing is deleted.
	c := newScheduleFlagsCmd()
	out := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesDeleteCmd.RunE(c, []string{"sch1"}); err == nil ||
			!strings.Contains(err.Error(), "aborted") {
			t.Errorf("expected aborted, got %v", err)
		}
	})
	if !strings.Contains(out, "Delete schedule sch1?") {
		t.Errorf("prompt missing:\n%s", out)
	}
	if len(stub.CallsFor("DELETE", schedulesPath+"/sch1")) != 0 {
		t.Error("aborted delete must not call the API")
	}

	// --yes goes straight through.
	c2 := newScheduleFlagsCmd()
	_ = c2.Flags().Set("yes", "true")
	out2 := captureStdoutCovCli2(t, func() {
		if err := routineSchedulesDeleteCmd.RunE(c2, []string{"sch1"}); err != nil {
			t.Errorf("delete: %v", err)
		}
	})
	if !strings.Contains(out2, "Schedule sch1 deleted.") {
		t.Errorf("output:\n%s", out2)
	}
	if len(stub.CallsFor("DELETE", schedulesPath+"/sch1")) != 1 {
		t.Error("DELETE not issued")
	}
}

// TestScheduleCmds_GuardsAndTransport sweeps the schedule RunE closures
// through no-auth, no-workspace, and dead-server branches.
func TestScheduleCmds_GuardsAndTransport(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{"list", func() error {
			c := newScheduleFlagsCmd()
			return routineSchedulesListCmd.RunE(c, nil)
		}},
		{"create", func() error {
			c := newScheduleFlagsCmd()
			_ = c.Flags().Set("slug", "x")
			_ = c.Flags().Set("cron", "* * * * *")
			return routineSchedulesCreateCmd.RunE(c, nil)
		}},
		{"update", func() error {
			c := newScheduleFlagsCmd()
			_ = c.Flags().Set("cron", "* * * * *")
			return routineSchedulesUpdateCmd.RunE(c, []string{"sch1"})
		}},
		{"now", func() error { return routineSchedulesNowCmd.RunE(routineSchedulesNowCmd, []string{"sch1"}) }},
		{"delete", func() error {
			c := newScheduleFlagsCmd()
			_ = c.Flags().Set("yes", "true")
			return routineSchedulesDeleteCmd.RunE(c, []string{"sch1"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("got %v", err)
			}
		})
		t.Run(tc.name+"/no workspace", func(t *testing.T) {
			saveCLIState(t)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tok"}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Errorf("got %v", err)
			}
		})
		t.Run(tc.name+"/dead server", func(t *testing.T) {
			setDeadCLI(t)
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "connection refused") {
				t.Errorf("got %v", err)
			}
		})
	}
}

func TestScheduleListAndCreateAndDelete_APIErrors(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setStubCLI(t, stub.URL())

	// list: CheckError branch + decode error branch.
	stub.OnGet(schedulesPath, clitest.ErrorResponse(500, "list broken"))
	c := newScheduleFlagsCmd()
	if err := routineSchedulesListCmd.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "list broken") {
		t.Errorf("list CheckError: got %v", err)
	}
	stub.OnGet(schedulesPath, clitest.TextResponse(200, "{not json"))
	c2 := newScheduleFlagsCmd()
	if err := routineSchedulesListCmd.RunE(c2, nil); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("list decode: got %v", err)
	}

	// create: CheckError branch.
	stub.OnPost(schedulesPath, clitest.ErrorResponse(422, "bad cron"))
	c3 := newScheduleFlagsCmd()
	_ = c3.Flags().Set("slug", "x")
	_ = c3.Flags().Set("cron", "nope")
	if err := routineSchedulesCreateCmd.RunE(c3, nil); err == nil || !strings.Contains(err.Error(), "bad cron") {
		t.Errorf("create CheckError: got %v", err)
	}

	// update: CheckError branch.
	stub.OnPatch(schedulesPath+"/schX", clitest.ErrorResponse(404, "no such schedule"))
	c4 := newScheduleFlagsCmd()
	_ = c4.Flags().Set("cron", "0 9 * * *")
	if err := routineSchedulesUpdateCmd.RunE(c4, []string{"schX"}); err == nil || !strings.Contains(err.Error(), "no such schedule") {
		t.Errorf("update CheckError: got %v", err)
	}

	// delete: CheckError branch.
	stub.OnDelete(schedulesPath+"/schX", clitest.ErrorResponse(403, "not yours"))
	c5 := newScheduleFlagsCmd()
	_ = c5.Flags().Set("yes", "true")
	if err := routineSchedulesDeleteCmd.RunE(c5, []string{"schX"}); err == nil || !strings.Contains(err.Error(), "not yours") {
		t.Errorf("delete CheckError: got %v", err)
	}
}
