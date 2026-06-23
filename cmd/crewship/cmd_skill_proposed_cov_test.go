package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSkillProposedList_Table(t *testing.T) {
	s := covStubCli9(t)
	longDesc := strings.Repeat("d", 70)
	s.OnGet("/api/v1/skills/proposed", clitest.JSONResponse(200, []proposedSkillRow{
		{FileName: "skill-deploy.md", Name: "deploy", Description: longDesc, DescriptionQuality: "good", Category: "ops"},
		{FileName: "skill-tidy.md", Name: "tidy", Description: "short", Category: "hygiene"}, // empty quality → "ok"
	}))
	covSetFlagCli9(t, skillProposedListCmd, "crew", covCrew)

	out := covCaptureStdoutCli9(t, func() {
		if err := runSkillProposedList(skillProposedListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"skill-deploy.md", "deploy", "ops", strings.Repeat("d", 57) + "...", "skill-tidy.md", "ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, longDesc) {
		t.Errorf("70-char description should be truncated:\n%s", out)
	}

	calls := s.CallsFor("GET", "/api/v1/skills/proposed")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "crew_id="+covCrew) {
		t.Errorf("crew_id not forwarded: %+v", calls)
	}
}

func TestSkillProposedList_JSON(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/skills/proposed", clitest.JSONResponse(200, []proposedSkillRow{
		{FileName: "skill-a.md", Name: "a"},
	}))
	covSetFlagCli9(t, skillProposedListCmd, "crew", covCrew)
	covSetFlagCli9(t, skillProposedListCmd, "format", "json")

	out := covCaptureStdoutCli9(t, func() {
		if err := runSkillProposedList(skillProposedListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var rows []proposedSkillRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--format=json output not JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0].FileName != "skill-a.md" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestSkillProposedList_Empty(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/skills/proposed", clitest.JSONResponse(200, []proposedSkillRow{}))
	covSetFlagCli9(t, skillProposedListCmd, "crew", covCrew)

	if err := runSkillProposedList(skillProposedListCmd, nil); err != nil {
		t.Fatalf("empty list should not error: %v", err)
	}
}

func TestSkillProposedApprove(t *testing.T) {
	for _, created := range []bool{true, false} {
		wantVerb := "updated"
		if created {
			wantVerb = "created"
		}
		t.Run(wantVerb, func(t *testing.T) {
			s := covStubCli9(t)
			s.OnPost("/api/v1/skills/proposed/approve", clitest.JSONResponse(200, map[string]any{
				"skill_id": "sk_1", "slug": "deploy", "created": created, "file_name": "skill-deploy.md",
			}))
			covSetFlagCli9(t, skillProposedApproveCmd, "crew", covCrew)
			covSetFlagCli9(t, skillProposedApproveCmd, "file", "skill-deploy.md")

			out := covCaptureStdoutCli9(t, func() {
				if err := runSkillProposedApprove(skillProposedApproveCmd, nil); err != nil {
					t.Errorf("RunE: %v", err)
				}
			})
			if !strings.Contains(out, "approved skill-deploy.md -> skill sk_1 (deploy, "+wantVerb+")") {
				t.Errorf("approve output wrong:\n%s", out)
			}

			calls := s.CallsFor("POST", "/api/v1/skills/proposed/approve")
			if len(calls) != 1 {
				t.Fatalf("expected one approve POST, got %d", len(calls))
			}
			var body proposedRequest
			_ = json.Unmarshal(calls[0].Body, &body)
			if body.CrewID != covCrew || body.FileName != "skill-deploy.md" {
				t.Errorf("approve body = %+v", body)
			}
		})
	}
}

func TestSkillProposedReject(t *testing.T) {
	for _, removed := range []bool{true, false} {
		t.Run(map[bool]string{true: "removed", false: "idempotent"}[removed], func(t *testing.T) {
			s := covStubCli9(t)
			s.OnPost("/api/v1/skills/proposed/reject", clitest.JSONResponse(200, map[string]any{
				"file_name": "skill-noise.md", "removed": removed,
			}))
			covSetFlagCli9(t, skillProposedRejectCmd, "crew", covCrew)
			covSetFlagCli9(t, skillProposedRejectCmd, "file", "skill-noise.md")

			out := covCaptureStdoutCli9(t, func() {
				if err := runSkillProposedReject(skillProposedRejectCmd, nil); err != nil {
					t.Errorf("RunE: %v", err)
				}
			})
			if removed && !strings.Contains(out, "staging file removed") {
				t.Errorf("expected removed message:\n%s", out)
			}
			if !removed && !strings.Contains(out, "already gone (idempotent)") {
				t.Errorf("expected idempotent message:\n%s", out)
			}
		})
	}
}

func TestSkillProposed_ErrorPaths(t *testing.T) {
	t.Run("list server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/skills/proposed", clitest.ErrorResponse(403, "not allowed"))
		covSetFlagCli9(t, skillProposedListCmd, "crew", covCrew)
		if err := runSkillProposedList(skillProposedListCmd, nil); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("expected permission error; got %v", err)
		}
	})
	t.Run("approve server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost("/api/v1/skills/proposed/approve", clitest.ErrorResponse(422, "license check failed"))
		covSetFlagCli9(t, skillProposedApproveCmd, "crew", covCrew)
		covSetFlagCli9(t, skillProposedApproveCmd, "file", "skill-x.md")
		if err := runSkillProposedApprove(skillProposedApproveCmd, nil); err == nil || !strings.Contains(err.Error(), "license check failed") {
			t.Errorf("expected import error; got %v", err)
		}
	})
	t.Run("reject server error", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost("/api/v1/skills/proposed/reject", clitest.ErrorResponse(500, "disk error"))
		covSetFlagCli9(t, skillProposedRejectCmd, "crew", covCrew)
		covSetFlagCli9(t, skillProposedRejectCmd, "file", "skill-x.md")
		if err := runSkillProposedReject(skillProposedRejectCmd, nil); err == nil || !strings.Contains(err.Error(), "disk error") {
			t.Errorf("expected server error; got %v", err)
		}
	})
	t.Run("auth gates", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := runSkillProposedList(skillProposedListCmd, nil); err == nil {
			t.Error("list: expected not-logged-in")
		}
		if err := runSkillProposedApprove(skillProposedApproveCmd, nil); err == nil {
			t.Error("approve: expected not-logged-in")
		}
		if err := runSkillProposedReject(skillProposedRejectCmd, nil); err == nil {
			t.Error("reject: expected not-logged-in")
		}
		cliCfg = &cli.CLIConfig{Token: "tok"}
		if err := runSkillProposedList(skillProposedListCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("list: expected workspace error; got %v", err)
		}
		if err := runSkillProposedApprove(skillProposedApproveCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("approve: expected workspace error; got %v", err)
		}
		if err := runSkillProposedReject(skillProposedRejectCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("reject: expected workspace error; got %v", err)
		}
	})
}
