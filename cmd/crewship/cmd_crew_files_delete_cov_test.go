package main

// Coverage tests for `crewship crew files delete` — drives the DELETE via
// the command's RunE against the clitest stub server, mirroring the save /
// get / list coverage in cmd_crew_files_cov_test.go. Red-first for #1391.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCrewFilesDeleteRunE(t *testing.T) {
	delPath := "/api/v1/crews/" + covCrewIDCli7 + "/files/delete"

	// --yes skips the interactive confirmation; set it for every case that
	// expects the request to actually fire, and restore afterwards.
	withYes := func(t *testing.T) {
		t.Helper()
		if err := crewFilesDeleteCmd.Flags().Set("yes", "true"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = crewFilesDeleteCmd.Flags().Set("yes", "false") })
	}

	t.Run("issues DELETE with path query and prints success", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnDelete(delPath, clitest.EmptyResponse(204))
		withYes(t)

		out, err := covCaptureStdoutCli7(t, func() error {
			return crewFilesDeleteCmd.RunE(crewFilesDeleteCmd, []string{"alpha", "shared/probe.sh"})
		})
		if err != nil {
			t.Fatalf("files delete: %v", err)
		}
		dels := s.CallsFor("DELETE", delPath)
		if len(dels) != 1 {
			t.Fatalf("expected 1 DELETE, got %d", len(dels))
		}
		if !strings.Contains(dels[0].Query, "path=shared%2Fprobe.sh") {
			t.Errorf("query = %q, want path=shared%%2Fprobe.sh", dels[0].Query)
		}
		if !strings.Contains(dels[0].Query, "workspace_id="+covWSCli7) {
			t.Errorf("workspace_id missing from query: %q", dels[0].Query)
		}
		_ = out
	})

	t.Run("server error propagates", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnDelete(delPath, clitest.ErrorResponse(404, "no such file"))
		withYes(t)

		err := crewFilesDeleteCmd.RunE(crewFilesDeleteCmd, []string{"alpha", "shared/ghost.txt"})
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("unknown crew", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		withYes(t)

		err := crewFilesDeleteCmd.RunE(crewFilesDeleteCmd, []string{"ghost", "p"})
		if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("no auth", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		err := crewFilesDeleteCmd.RunE(crewFilesDeleteCmd, []string{"alpha", "p"})
		if err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("no workspace", func(t *testing.T) {
		covNoWorkspaceCLI(t)
		err := crewFilesDeleteCmd.RunE(crewFilesDeleteCmd, []string{"alpha", "p"})
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("got %v", err)
		}
	})
}
