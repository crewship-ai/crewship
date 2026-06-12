package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covWaitpointsPath = "/api/v1/workspaces/" + covWSCli9 + "/pipelines/waitpoints"

func covWaitpointRows() []waitpointRow {
	return []waitpointRow{
		{
			Token:         "wp_tok_full_1234567890",
			PipelineRunID: "run_aaaaaaaaaaaaaaaa",
			StepID:        "approve-step",
			Kind:          "approval",
			Prompt:        strings.Repeat("p", 80), // forces the 60-char truncation branch
			TimeoutAt:     "2026-06-12T10:00:00Z",
			CreatedAt:     "2026-06-12T09:00:00Z",
		},
		{
			Token:          "wp_tok_short",
			PipelineRunID:  "run_bbbbbbbbbbbbbbbb",
			StepID:         "gate",
			Kind:           "approval",
			Prompt:         "short prompt",
			InvokingCrewID: "crew_invoker",
			TimeoutAt:      "2026-06-12T11:00:00Z",
			CreatedAt:      "2026-06-12T09:30:00Z",
		},
	}
}

func TestWaitpointsList_Table(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet(covWaitpointsPath, clitest.JSONResponse(200, covWaitpointRows()))

	out := covCaptureStdoutCli9(t, func() {
		if err := routineWaitpointsListCmd.RunE(routineWaitpointsListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "TOKEN") {
		t.Errorf("missing header:\n%s", out)
	}
	// Full token must be shown (it is the action argument).
	if !strings.Contains(out, "wp_tok_full_1234567890") {
		t.Errorf("full token should not be truncated:\n%s", out)
	}
	// Long prompt truncated to 57 chars + "...".
	if !strings.Contains(out, strings.Repeat("p", 57)+"...") {
		t.Errorf("long prompt should be truncated:\n%s", out)
	}
	if strings.Contains(out, strings.Repeat("p", 80)) {
		t.Errorf("80-char prompt should not appear verbatim:\n%s", out)
	}
}

func TestWaitpointsList_JSON(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet(covWaitpointsPath, clitest.JSONResponse(200, covWaitpointRows()))
	covSetFlagCli9(t, routineWaitpointsListCmd, "json", "true")

	out := covCaptureStdoutCli9(t, func() {
		if err := routineWaitpointsListCmd.RunE(routineWaitpointsListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var rows []waitpointRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if len(rows) != 2 || rows[0].Token != "wp_tok_full_1234567890" {
		t.Errorf("rows roundtrip wrong: %+v", rows)
	}
}

func TestWaitpointsList_Empty(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet(covWaitpointsPath, clitest.JSONResponse(200, []waitpointRow{}))

	out := covCaptureStdoutCli9(t, func() {
		if err := routineWaitpointsListCmd.RunE(routineWaitpointsListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "No pending waitpoints.") {
		t.Errorf("missing empty-state line:\n%s", out)
	}
}

func TestWaitpointsList_ErrorPaths(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet(covWaitpointsPath, clitest.ErrorResponse(500, "boom"))
		if err := routineWaitpointsListCmd.RunE(routineWaitpointsListCmd, nil); err == nil {
			t.Error("expected 500 to surface as error")
		}
	})
	t.Run("decode error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet(covWaitpointsPath, clitest.TextResponse(200, "{nope"))
		err := routineWaitpointsListCmd.RunE(routineWaitpointsListCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Errorf("expected decode error; got %v", err)
		}
	})
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := routineWaitpointsListCmd.RunE(routineWaitpointsListCmd, nil); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
}

func TestWaitpointsShow_FoundWithCrew(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet(covWaitpointsPath, clitest.JSONResponse(200, covWaitpointRows()))

	out := covCaptureStdoutCli9(t, func() {
		if err := routineWaitpointsShowCmd.RunE(routineWaitpointsShowCmd, []string{"wp_tok_short"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Token:", "wp_tok_short", "Invoking crew:", "crew_invoker", "Prompt:", "short prompt"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q:\n%s", want, out)
		}
	}
}

func TestWaitpointsShow_NotFound(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet(covWaitpointsPath, clitest.JSONResponse(200, covWaitpointRows()))

	err := routineWaitpointsShowCmd.RunE(routineWaitpointsShowCmd, []string{"missing-token"})
	if err == nil || !strings.Contains(err.Error(), "waitpoint missing-token not found") {
		t.Errorf("expected not-found error; got %v", err)
	}
}

func TestWaitpointsApprove_PostsDecision(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost(covWaitpointsPath+"/tok-1/approve", clitest.JSONResponse(200, map[string]string{"ok": "1"}))
	covSetFlagCli9(t, routineWaitpointsApproveCmd, "comment", "LGTM")

	out := covCaptureStdoutCli9(t, func() {
		if err := routineWaitpointsApproveCmd.RunE(routineWaitpointsApproveCmd, []string{"tok-1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Approved waitpoint") {
		t.Errorf("missing approval confirmation:\n%s", out)
	}

	calls := s.CallsFor("POST", covWaitpointsPath+"/tok-1/approve")
	if len(calls) != 1 {
		t.Fatalf("expected exactly one approve POST, got %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("approve body not JSON: %v", err)
	}
	if body["approved"] != true || body["comment"] != "LGTM" {
		t.Errorf("approve body = %v, want approved=true comment=LGTM", body)
	}
}

func TestWaitpointsReject_PostsDecision(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost(covWaitpointsPath+"/tok-2/approve", clitest.JSONResponse(200, map[string]string{"ok": "1"}))
	covSetFlagCli9(t, routineWaitpointsRejectCmd, "comment", "needs work")

	out := covCaptureStdoutCli9(t, func() {
		if err := routineWaitpointsRejectCmd.RunE(routineWaitpointsRejectCmd, []string{"tok-2"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Rejected waitpoint") {
		t.Errorf("missing rejection confirmation:\n%s", out)
	}

	calls := s.CallsFor("POST", covWaitpointsPath+"/tok-2/approve")
	if len(calls) != 1 {
		t.Fatalf("expected exactly one reject POST, got %d", len(calls))
	}
	var body map[string]any
	_ = json.Unmarshal(calls[0].Body, &body)
	if body["approved"] != false || body["comment"] != "needs work" {
		t.Errorf("reject body = %v, want approved=false comment='needs work'", body)
	}
}

func TestDecideWaitpoint_Errors(t *testing.T) {
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := decideWaitpoint("tok", true, ""); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{Token: "tok"}
		err := decideWaitpoint("tok", true, "")
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("expected workspace error; got %v", err)
		}
	})
	t.Run("server rejects", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost(covWaitpointsPath+"/tok-3/approve", clitest.ErrorResponse(409, "already decided"))
		err := decideWaitpoint("tok-3", false, "")
		if err == nil || !strings.Contains(err.Error(), "already decided") {
			t.Errorf("expected server error to surface; got %v", err)
		}
	})
}
