package main

// Coverage tests for cmd_mission_mutate.go — create / update / delete /
// clone.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

const (
	covMissionCrew   = "ccrewa1234567890123456789"
	covMissionIDCli7 = "cmiss1234567890123456789"
	covLeadID        = "clead1234567890123456789"
)

// covMissionStubs registers crews + agents + missions lists used by the
// resolve helpers.
func covMissionStubs(s *clitest.StubServer) {
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covMissionCrew, "slug": "alpha"},
	}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covLeadID, "slug": "viktor", "agent_role": "LEAD", "crew_id": covMissionCrew},
		{"id": "cpeer1234567890123456789", "slug": "eva", "agent_role": "AGENT", "crew_id": covMissionCrew},
	}))
	s.OnGet("/api/v1/missions", clitest.JSONResponse(200, []map[string]string{
		{"id": covMissionIDCli7, "crew_id": covMissionCrew},
	}))
}

func covResetMissionCreateFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = missionCreateCmd.Flags().Set("title", "")
		_ = missionCreateCmd.Flags().Set("description", "")
		_ = missionCreateCmd.Flags().Set("crew", "")
		_ = missionCreateCmd.Flags().Set("lead", "")
	})
}

func TestMissionMutate_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"create", missionCreateCmd, nil},
		{"update", missionUpdateCmd, []string{covMissionIDCli7}},
		{"delete", missionDeleteCmd, []string{covMissionIDCli7}},
		{"clone", missionCloneCmd, []string{covMissionIDCli7}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", tc.name, err)
		}
	}
}

func TestMissionCreateRunE(t *testing.T) {
	missionsPath := "/api/v1/crews/" + covMissionCrew + "/missions"

	t.Run("title required", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covResetMissionCreateFlags(t)
		err := missionCreateCmd.RunE(missionCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "--title is required") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("crew required", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "Chart the reef")
		err := missionCreateCmd.RunE(missionCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "--crew is required") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("auto-detects LEAD agent", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnPost(missionsPath, clitest.JSONResponse(201, map[string]string{"id": "m-new", "title": "Chart the reef"}))
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "Chart the reef")
		_ = missionCreateCmd.Flags().Set("crew", "alpha")
		_ = missionCreateCmd.Flags().Set("description", "carefully")

		if err := missionCreateCmd.RunE(missionCreateCmd, nil); err != nil {
			t.Fatalf("create: %v", err)
		}
		posts := s.CallsFor("POST", missionsPath)
		if len(posts) != 1 {
			t.Fatalf("POSTs = %d", len(posts))
		}
		var body map[string]any
		_ = json.Unmarshal(posts[0].Body, &body)
		if body["title"] != "Chart the reef" || body["lead_agent_id"] != covLeadID || body["description"] != "carefully" {
			t.Errorf("create body = %v", body)
		}
	})

	t.Run("explicit --lead slug overrides detection", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnPost(missionsPath, clitest.JSONResponse(201, map[string]string{"id": "m-new", "title": "T"}))
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "T")
		_ = missionCreateCmd.Flags().Set("crew", "alpha")
		_ = missionCreateCmd.Flags().Set("lead", "eva")

		if err := missionCreateCmd.RunE(missionCreateCmd, nil); err != nil {
			t.Fatalf("create: %v", err)
		}
		posts := s.CallsFor("POST", missionsPath)
		var body map[string]any
		_ = json.Unmarshal(posts[len(posts)-1].Body, &body)
		if body["lead_agent_id"] != "cpeer1234567890123456789" {
			t.Errorf("lead_agent_id = %v, want eva's id", body["lead_agent_id"])
		}
	})

	t.Run("no LEAD in crew", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
			{"id": covMissionCrew, "slug": "alpha"},
		}))
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": "ca1234567890123456789012", "slug": "eva", "agent_role": "AGENT", "crew_id": covMissionCrew},
		}))
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "T")
		_ = missionCreateCmd.Flags().Set("crew", "alpha")
		err := missionCreateCmd.RunE(missionCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "no LEAD agent found") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestMissionUpdateRunE(t *testing.T) {
	patchPath := "/api/v1/crews/" + covMissionCrew + "/missions/" + covMissionIDCli7

	resetUpdateFlags := func(t *testing.T) {
		t.Helper()
		t.Cleanup(func() {
			for _, f := range []string{"title", "description", "status"} {
				_ = missionUpdateCmd.Flags().Set(f, "")
				missionUpdateCmd.Flags().Lookup(f).Changed = false
			}
		})
	}

	t.Run("no fields to update", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		resetUpdateFlags(t)
		err := missionUpdateCmd.RunE(missionUpdateCmd, []string{covMissionIDCli7})
		if err == nil || !strings.Contains(err.Error(), "no fields to update") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("patches changed fields by id prefix", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{}))
		resetUpdateFlags(t)
		_ = missionUpdateCmd.Flags().Set("title", "New title")
		_ = missionUpdateCmd.Flags().Set("status", "IN_PROGRESS")

		// 8+ char prefix of the mission id resolves to the full id.
		if err := missionUpdateCmd.RunE(missionUpdateCmd, []string{covMissionIDCli7[:10]}); err != nil {
			t.Fatalf("update: %v", err)
		}
		patches := s.CallsFor("PATCH", patchPath)
		if len(patches) != 1 {
			t.Fatalf("PATCHes = %d", len(patches))
		}
		var body map[string]any
		_ = json.Unmarshal(patches[0].Body, &body)
		if body["title"] != "New title" || body["status"] != "IN_PROGRESS" {
			t.Errorf("patch body = %v", body)
		}
		if _, has := body["description"]; has {
			t.Errorf("unchanged description must not be sent: %v", body)
		}
	})

	t.Run("unknown mission", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		resetUpdateFlags(t)
		_ = missionUpdateCmd.Flags().Set("title", "X")
		err := missionUpdateCmd.RunE(missionUpdateCmd, []string{"cmissUNKNOWN123456789012"})
		if err == nil || !strings.Contains(err.Error(), "mission not found") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestMissionDeleteRunE(t *testing.T) {
	deletePath := "/api/v1/crews/" + covMissionCrew + "/missions/" + covMissionIDCli7

	t.Run("with --yes deletes", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnDelete(deletePath, clitest.EmptyResponse(204))
		if err := missionDeleteCmd.Flags().Set("yes", "true"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = missionDeleteCmd.Flags().Set("yes", "false") })

		if err := missionDeleteCmd.RunE(missionDeleteCmd, []string{covMissionIDCli7}); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if n := len(s.CallsFor("DELETE", deletePath)); n != 1 {
			t.Errorf("DELETEs = %d", n)
		}
	})

	t.Run("without --yes aborts on empty stdin", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		_ = missionDeleteCmd.Flags().Set("yes", "false")

		err := missionDeleteCmd.RunE(missionDeleteCmd, []string{covMissionIDCli7})
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Fatalf("got %v", err)
		}
		for _, c := range s.Calls() {
			if c.Method == "DELETE" {
				t.Error("aborted delete must not hit the API")
			}
		}
	})
}

func TestMissionCloneRunE(t *testing.T) {
	clonePath := "/api/v1/crews/" + covMissionCrew + "/missions/" + covMissionIDCli7 + "/clone"

	t.Run("clone with title override", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnPost(clonePath, clitest.JSONResponse(201, map[string]string{
			"id": "m-clone", "title": "Copy of mission", "status": "PLANNING",
		}))
		if err := missionCloneCmd.Flags().Set("title", "Copy of mission"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = missionCloneCmd.Flags().Set("title", "") })

		if err := missionCloneCmd.RunE(missionCloneCmd, []string{covMissionIDCli7}); err != nil {
			t.Fatalf("clone: %v", err)
		}
		posts := s.CallsFor("POST", clonePath)
		if len(posts) != 1 {
			t.Fatalf("POSTs = %d", len(posts))
		}
		var body map[string]string
		_ = json.Unmarshal(posts[0].Body, &body)
		if body["title"] != "Copy of mission" {
			t.Errorf("clone body = %v", body)
		}
	})

	t.Run("clone API error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnPost(clonePath, clitest.ErrorResponse(500, "clone failed"))
		_ = missionCloneCmd.Flags().Set("title", "")
		err := missionCloneCmd.RunE(missionCloneCmd, []string{covMissionIDCli7})
		if err == nil || !strings.Contains(err.Error(), "clone failed") {
			t.Fatalf("got %v", err)
		}
	})
}

// ─── additional error paths ──────────────────────────────────────────────

func TestMissionMutate_NoWorkspace(t *testing.T) {
	covNoWorkspaceCLI(t)

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"create", missionCreateCmd, nil},
		{"update", missionUpdateCmd, []string{covMissionIDCli7}},
		{"delete", missionDeleteCmd, []string{covMissionIDCli7}},
		{"clone", missionCloneCmd, []string{covMissionIDCli7}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error, got %v", tc.name, err)
		}
	}
}

func TestMissionCreateRunE_ResolveErrors(t *testing.T) {
	t.Run("unknown crew", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "T")
		_ = missionCreateCmd.Flags().Set("crew", "ghost")
		err := missionCreateCmd.RunE(missionCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("unknown lead", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "T")
		_ = missionCreateCmd.Flags().Set("crew", "alpha")
		_ = missionCreateCmd.Flags().Set("lead", "ghost")
		err := missionCreateCmd.RunE(missionCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("create rejected by API", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnPost("/api/v1/crews/"+covMissionCrew+"/missions", clitest.ErrorResponse(422, "title too long"))
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "T")
		_ = missionCreateCmd.Flags().Set("crew", "alpha")
		err := missionCreateCmd.RunE(missionCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "title too long") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("undecodable create response", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnPost("/api/v1/crews/"+covMissionCrew+"/missions", clitest.TextResponse(200, "x"))
		covResetMissionCreateFlags(t)
		_ = missionCreateCmd.Flags().Set("title", "T")
		_ = missionCreateCmd.Flags().Set("crew", "alpha")
		if err := missionCreateCmd.RunE(missionCreateCmd, nil); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestMissionUpdateRunE_DescriptionAndAPIError(t *testing.T) {
	patchPath := "/api/v1/crews/" + covMissionCrew + "/missions/" + covMissionIDCli7

	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covMissionStubs(s)
	s.OnPatch(patchPath, clitest.ErrorResponse(409, "mission locked"))
	_ = missionUpdateCmd.Flags().Set("description", "new desc")
	t.Cleanup(func() {
		_ = missionUpdateCmd.Flags().Set("description", "")
		missionUpdateCmd.Flags().Lookup("description").Changed = false
	})

	err := missionUpdateCmd.RunE(missionUpdateCmd, []string{covMissionIDCli7})
	if err == nil || !strings.Contains(err.Error(), "mission locked") {
		t.Fatalf("got %v", err)
	}
	patches := s.CallsFor("PATCH", patchPath)
	if len(patches) != 1 || !strings.Contains(string(patches[0].Body), `"description":"new desc"`) {
		t.Errorf("patch body = %+v", patches)
	}
}

func TestMissionDeleteCloneRunE_ResolveAndAPIErrors(t *testing.T) {
	t.Run("delete unknown mission", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		_ = missionDeleteCmd.Flags().Set("yes", "true")
		t.Cleanup(func() { _ = missionDeleteCmd.Flags().Set("yes", "false") })
		err := missionDeleteCmd.RunE(missionDeleteCmd, []string{"cmissUNKNOWN123456789012"})
		if err == nil || !strings.Contains(err.Error(), "mission not found") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("delete rejected by API", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		s.OnDelete("/api/v1/crews/"+covMissionCrew+"/missions/"+covMissionIDCli7, clitest.ErrorResponse(409, "mission running"))
		_ = missionDeleteCmd.Flags().Set("yes", "true")
		t.Cleanup(func() { _ = missionDeleteCmd.Flags().Set("yes", "false") })
		err := missionDeleteCmd.RunE(missionDeleteCmd, []string{covMissionIDCli7})
		if err == nil || !strings.Contains(err.Error(), "mission running") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("clone unknown mission", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covMissionStubs(s)
		err := missionCloneCmd.RunE(missionCloneCmd, []string{"cmissUNKNOWN123456789012"})
		if err == nil || !strings.Contains(err.Error(), "mission not found") {
			t.Fatalf("got %v", err)
		}
	})
}
