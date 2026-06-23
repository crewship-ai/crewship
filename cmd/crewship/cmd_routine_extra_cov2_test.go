package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func routineGuardCases() []covCmdCase {
	return []covCmdCase{
		{name: "versions", cmd: routineVersionsCmd, args: []string{"slug"}},
		{name: "versions show", cmd: routineVersionsShowCmd, args: []string{"slug"},
			flags: map[string]string{"version": "1"}},
		{name: "active", cmd: routineActiveCmd},
		{name: "rollback", cmd: routineRollbackCmd, args: []string{"slug"},
			flags: map[string]string{"to": "1"}},
		{name: "export", cmd: routineExportCmd, args: []string{"slug"}},
		{name: "cancel", cmd: routineCancelCmd, args: []string{"run-1"}},
	}
}

func TestRoutineExtraCmds_NoAuth(t *testing.T) {
	cases := append(routineGuardCases(),
		covCmdCase{name: "import", cmd: routineImportCmd})
	covRunNoAuth(t, cases)
}

func TestRoutineExtraCmds_NoWorkspace(t *testing.T) {
	cases := append(routineGuardCases(),
		covCmdCase{name: "import", cmd: routineImportCmd})
	covRunNoWorkspace(t, cases)
}

func TestRoutineExtraCmds_TransportError(t *testing.T) {
	covRunTransportError(t, routineGuardCases())
}

func TestRoutineImportCmd_TransportError(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	covWithStdin(t, `{"slug":"x"}`)
	cliCfg = &cli.CLIConfig{Token: "tk", Workspace: covWSCli3, Server: "http://127.0.0.1:1"}
	if err := routineImportCmd.RunE(routineImportCmd, nil); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestRoutineVersionsCmd_MalformedResponse(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/pipelines/slug/versions",
		clitest.TextResponse(200, "[broken"))
	if err := routineVersionsCmd.RunE(routineVersionsCmd, []string{"slug"}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestRoutineActiveCmd_MalformedResponse(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/pipelines/runs/active",
		clitest.TextResponse(200, "[broken"))
	if err := routineActiveCmd.RunE(routineActiveCmd, nil); err == nil {
		t.Fatal("expected decode error")
	}
}
