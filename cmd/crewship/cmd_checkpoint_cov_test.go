package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covCheckpointSubs enumerates every checkpoint subcommand with valid
// args + required flags so gate/transport sweeps stay table-driven.
func covCheckpointSubs(t *testing.T) map[string]func() error {
	t.Helper()
	covSetFlagCli9(t, checkpointListCmd, "mission", "MIS-1")
	covSetFlagCli9(t, checkpointCreateCmd, "mission", "MIS-1")
	covSetFlagCli9(t, checkpointDeleteCmd, "yes", "true")
	run := func(cmd *cobra.Command, args []string) func() error {
		return func() error { return cmd.RunE(cmd, args) }
	}
	return map[string]func() error{
		"list":    run(checkpointListCmd, nil),
		"create":  run(checkpointCreateCmd, nil),
		"restore": run(checkpointRestoreCmd, []string{"chk_1"}),
		"fork":    run(checkpointForkCmd, []string{"chk_1"}),
		"delete":  run(checkpointDeleteCmd, []string{"chk_1"}),
	}
}

func TestCheckpointCmds_AuthGates(t *testing.T) {
	covSaveState(t)
	subs := covCheckpointSubs(t)
	for name, invoke := range subs {
		cliCfg = &cli.CLIConfig{}
		if err := invoke(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected not-logged-in; got %v", name, err)
		}
		cliCfg = &cli.CLIConfig{Token: "tok"}
		if err := invoke(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error; got %v", name, err)
		}
	}
}

func TestCheckpointCmds_TransportErrors(t *testing.T) {
	covStubDown(t)
	for name, invoke := range covCheckpointSubs(t) {
		if err := invoke(); err == nil {
			t.Errorf("%s: expected transport error against dead server", name)
		}
	}
}

func TestCheckpointCmds_DecodeErrors(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/missions/MIS-1/checkpoints", clitest.TextResponse(200, "{nope"))
	s.OnPost("/api/v1/missions/MIS-1/checkpoints", clitest.TextResponse(200, "{nope"))
	s.OnPost("/api/v1/checkpoints/chk_1/restore", clitest.TextResponse(200, "{nope"))
	s.OnPost("/api/v1/checkpoints/chk_1/fork", clitest.TextResponse(200, "{nope"))
	subs := covCheckpointSubs(t)
	for _, name := range []string{"list", "create", "restore", "fork"} {
		if err := subs[name](); err == nil {
			t.Errorf("%s: expected decode error on malformed 200 body", name)
		}
	}
}

func TestCheckpointCreateAndRestore_YAMLFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/missions/MIS-1/checkpoints", clitest.JSONResponse(201, checkpointRow{ID: "chk_yaml"}))
	s.OnPost("/api/v1/checkpoints/chk_yaml/restore", clitest.JSONResponse(200, map[string]any{
		"checkpoint": checkpointRow{ID: "chk_yaml"}, "journal_cursor": "cur_2",
	}))
	s.OnPost("/api/v1/checkpoints/chk_yaml/fork", clitest.JSONResponse(200, map[string]string{"new_mission_id": "MIS-2"}))
	covSetFlagCli9(t, checkpointCreateCmd, "mission", "MIS-1")
	flagFormat = "yaml"

	out := covCaptureStdoutCli9(t, func() {
		if err := checkpointCreateCmd.RunE(checkpointCreateCmd, nil); err != nil {
			t.Errorf("create: %v", err)
		}
		if err := checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_yaml"}); err != nil {
			t.Errorf("restore: %v", err)
		}
		if err := checkpointForkCmd.RunE(checkpointForkCmd, []string{"chk_yaml"}); err != nil {
			t.Errorf("fork: %v", err)
		}
	})
	if !strings.Contains(out, "chk_yaml") || !strings.Contains(out, "MIS-2") {
		t.Errorf("yaml output incomplete:\n%s", out)
	}
}

func TestCheckpointRestoreRunE_WithDivergence(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/checkpoints/chk_1/restore", clitest.JSONResponse(200, map[string]any{
		"checkpoint":      checkpointRow{ID: "chk_1", Label: "green build", JournalCursor: "cur_5"},
		"journal_cursor":  "cur_5",
		"warn_divergence": []string{"entry-6 mutated state", "entry-7 mutated state"},
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Checkpoint:", "chk_1", "Cursor:", "cur_5", "Diverged entries (2):", "entry-6 mutated state"} {
		if !strings.Contains(out, want) {
			t.Errorf("restore output missing %q:\n%s", want, out)
		}
	}
}

func TestCheckpointRestoreRunE_NoDivergence(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/checkpoints/chk_2/restore", clitest.JSONResponse(200, map[string]any{
		"checkpoint":     checkpointRow{ID: "chk_2"},
		"journal_cursor": "cur_9",
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_2"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "No divergence since checkpoint.") {
		t.Errorf("missing no-divergence line:\n%s", out)
	}
}

func TestCheckpointRestoreRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/checkpoints/chk_3/restore", clitest.JSONResponse(200, map[string]any{
		"checkpoint":     checkpointRow{ID: "chk_3"},
		"journal_cursor": "cur_1",
	}))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_3"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"chk_3"`) {
		t.Errorf("json output missing checkpoint id:\n%s", out)
	}
}

func TestCheckpointForkRunE(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/checkpoints/chk_1/fork", clitest.JSONResponse(200, map[string]string{
		"new_mission_id": "MIS-99", "new_checkpoint_id": "chk_fork",
	}))
	covSetFlagCli9(t, checkpointForkCmd, "label", "experiment-1")

	stderr := covCaptureStderrCli9(t, func() {
		if err := checkpointForkCmd.RunE(checkpointForkCmd, []string{"chk_1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "Forked mission MIS-99 from checkpoint chk_1 (new checkpoint chk_fork).") {
		t.Errorf("fork confirmation missing:\n%s", stderr)
	}

	calls := s.CallsFor("POST", "/api/v1/checkpoints/chk_1/fork")
	if len(calls) != 1 {
		t.Fatalf("expected one fork POST, got %d", len(calls))
	}
	var body map[string]string
	_ = json.Unmarshal(calls[0].Body, &body)
	if body["label"] != "experiment-1" {
		t.Errorf("fork body = %v", body)
	}
}

func TestCheckpointForkRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/checkpoints/chk_4/fork", clitest.JSONResponse(200, map[string]string{
		"new_mission_id": "MIS-100", "new_checkpoint_id": "chk_f2",
	}))
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := checkpointForkCmd.RunE(checkpointForkCmd, []string{"chk_4"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"MIS-100"`) {
		t.Errorf("json output missing mission id:\n%s", out)
	}
}

func TestCheckpointCreateRunE_JSONFormat(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/missions/MIS-42/checkpoints", clitest.JSONResponse(201, checkpointRow{
		ID: "chk_new", Label: "green", JournalCursor: "cur_7",
	}))
	covSetFlagCli9(t, checkpointCreateCmd, "mission", "MIS-42")
	covSetFlagCli9(t, checkpointCreateCmd, "label", "green")
	flagFormat = "json"

	out := covCaptureStdoutCli9(t, func() {
		if err := checkpointCreateCmd.RunE(checkpointCreateCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"chk_new"`) {
		t.Errorf("json output missing id:\n%s", out)
	}
	calls := s.CallsFor("POST", "/api/v1/missions/MIS-42/checkpoints")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"label":"green"`) {
		t.Errorf("create body wrong: %+v", calls)
	}
}

func TestCheckpointDeleteRunE_Abort(t *testing.T) {
	s := covStubCli9(t)
	covSetFlagCli9(t, checkpointDeleteCmd, "yes", "false")

	var err error
	_ = covCaptureStderrCli9(t, func() {
		err = checkpointDeleteCmd.RunE(checkpointDeleteCmd, []string{"chk_1"})
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted; got %v", err)
	}
	if got := len(s.Calls()); got != 0 {
		t.Errorf("aborted delete must not hit the API (%d calls)", got)
	}
}

func TestCheckpointCmds_ServerErrors(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/missions/MIS-1/checkpoints", clitest.ErrorResponse(404, "mission missing"))
	s.OnPost("/api/v1/checkpoints/chk_x/restore", clitest.ErrorResponse(404, "checkpoint missing"))
	s.OnPost("/api/v1/checkpoints/chk_x/fork", clitest.ErrorResponse(409, "fork conflict"))
	s.OnDelete("/api/v1/checkpoints/chk_x", clitest.ErrorResponse(404, "checkpoint missing"))
	covSetFlagCli9(t, checkpointListCmd, "mission", "MIS-1")
	covSetFlagCli9(t, checkpointDeleteCmd, "yes", "true")

	if err := checkpointListCmd.RunE(checkpointListCmd, nil); err == nil || !strings.Contains(err.Error(), "mission missing") {
		t.Errorf("list: expected error; got %v", err)
	}
	if err := checkpointRestoreCmd.RunE(checkpointRestoreCmd, []string{"chk_x"}); err == nil || !strings.Contains(err.Error(), "checkpoint missing") {
		t.Errorf("restore: expected error; got %v", err)
	}
	if err := checkpointForkCmd.RunE(checkpointForkCmd, []string{"chk_x"}); err == nil || !strings.Contains(err.Error(), "fork conflict") {
		t.Errorf("fork: expected error; got %v", err)
	}
	if err := checkpointDeleteCmd.RunE(checkpointDeleteCmd, []string{"chk_x"}); err == nil || !strings.Contains(err.Error(), "checkpoint missing") {
		t.Errorf("delete: expected error; got %v", err)
	}
}
