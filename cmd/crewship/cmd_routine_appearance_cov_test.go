package main

// Coverage for cmd_routine_appearance.go (`routine appearance get/set`).
// routineAppearanceCmd's own name never appears in the test suite even
// though its children carry real logic (which fields to send, when --clear
// conflicts with --icon/--color) — the gap this file closes.
//
// Fixtures are shaped from PipelineHandler.SetAppearance /
// toPipelineResponse in internal/api/pipelines_appearance.go: the wire
// response is the pipeline row (slug/name/icon/color), not the request body
// echoed back.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineAppearanceGetRunE_Set(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/monthly-report",
		clitest.JSONResponse(200, map[string]any{
			"slug": "monthly-report", "name": "Monthly Report", "icon": "receipt", "color": "amber",
		}))

	out := humanOut(t, func() error {
		return routineAppearanceGetCmd.RunE(routineAppearanceGetCmd, []string{"monthly-report"})
	})
	if !strings.Contains(out, "icon:  receipt") || !strings.Contains(out, "color: amber") {
		t.Errorf("human output missing icon/color: %q", out)
	}

	doc := jsonOut(t, func() error {
		return routineAppearanceGetCmd.RunE(routineAppearanceGetCmd, []string{"monthly-report"})
	})
	row, ok := doc.(map[string]any)
	if !ok || row["icon"] != "receipt" || row["color"] != "amber" {
		t.Errorf("json output = %#v", doc)
	}
}

func TestRoutineAppearanceGetRunE_Unset(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/plain-routine",
		clitest.JSONResponse(200, map[string]any{"slug": "plain-routine", "name": "Plain"}))

	out := humanOut(t, func() error {
		return routineAppearanceGetCmd.RunE(routineAppearanceGetCmd, []string{"plain-routine"})
	})
	if !strings.Contains(out, "no icon set") {
		t.Errorf("output should say no icon set: %q", out)
	}
}

// ─── set: validation ──────────────────────────────────────────────────────

func TestRoutineAppearanceSetRunE_NoFlagsIsError(t *testing.T) {
	covSetupCli5(t)
	err := routineAppearanceSetCmd.RunE(routineAppearanceSetCmd, []string{"my-routine"})
	if err == nil || !strings.Contains(err.Error(), "give --icon, --color, or --clear") {
		t.Errorf("want no-flags error, got %v", err)
	}
}

func TestRoutineAppearanceSetRunE_ClearCombinedWithIconIsError(t *testing.T) {
	covSetupCli5(t)
	if err := routineAppearanceSetCmd.Flags().Set("clear", "true"); err != nil {
		t.Fatal(err)
	}
	if err := routineAppearanceSetCmd.Flags().Set("icon", "rocket"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = routineAppearanceSetCmd.Flags().Set("clear", "false")
		_ = routineAppearanceSetCmd.Flags().Set("icon", "")
	})
	err := routineAppearanceSetCmd.RunE(routineAppearanceSetCmd, []string{"my-routine"})
	if err == nil || !strings.Contains(err.Error(), "--clear cannot be combined") {
		t.Errorf("want combination error, got %v", err)
	}
}

// ─── set: happy paths ─────────────────────────────────────────────────────

// Only the flags actually given must be sent — the endpoint treats an
// absent field as "leave alone", so setting the color while leaving the
// icon untouched must NOT wipe the icon (#appearanceBody are pointers
// server-side precisely for this).
func TestRoutineAppearanceSetRunE_OnlySendsChangedFields(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPatch("/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/appearance",
		clitest.JSONResponse(200, map[string]any{"slug": "my-routine", "name": "x", "color": "violet"}))
	if err := routineAppearanceSetCmd.Flags().Set("color", "violet"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routineAppearanceSetCmd.Flags().Set("color", "") })

	covCaptureStdoutCli5(t, func() {
		if err := routineAppearanceSetCmd.RunE(routineAppearanceSetCmd, []string{"my-routine"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("PATCH", "/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/appearance")
	if len(calls) != 1 {
		t.Fatalf("want 1 PATCH call, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if _, hasIcon := body["icon"]; hasIcon {
		t.Errorf("icon must be absent from body when only --color was given: %v", body)
	}
	if body["color"] != "violet" {
		t.Errorf("color = %v, want violet", body["color"])
	}
}

func TestRoutineAppearanceSetRunE_Clear(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPatch("/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/appearance",
		clitest.JSONResponse(200, map[string]any{"slug": "my-routine", "name": "x"}))
	if err := routineAppearanceSetCmd.Flags().Set("clear", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routineAppearanceSetCmd.Flags().Set("clear", "false") })

	out := humanOut(t, func() error {
		return routineAppearanceSetCmd.RunE(routineAppearanceSetCmd, []string{"my-routine"})
	})
	if !strings.Contains(out, "Cleared my-routine") {
		t.Errorf("output = %q", out)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/workspaces/"+covWSCli5+"/pipelines/my-routine/appearance")
	if len(calls) != 1 {
		t.Fatalf("want 1 PATCH call, got %d", len(calls))
	}
	body := covJSONBody(t, calls[0].Body)
	if body["icon"] != "" || body["color"] != "" {
		t.Errorf("clear must send empty-string icon+color, got %v", body)
	}
}
