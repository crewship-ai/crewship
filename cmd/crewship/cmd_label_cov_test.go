package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func covLabelSubs(t *testing.T) map[string]func() error {
	t.Helper()
	covSetFlagCli9(t, labelCreateCmd, "name", "n")
	covSetFlagCli9(t, labelCreateCmd, "color", "#abc")
	covSetFlagCli9(t, labelUpdateCmd, "name", "n2")
	covSetFlagCli9(t, labelDeleteCmd, "yes", "true")
	run := func(cmd *cobra.Command, args []string) func() error {
		return func() error { return cmd.RunE(cmd, args) }
	}
	return map[string]func() error{
		"list":   run(labelListCmd, nil),
		"create": run(labelCreateCmd, nil),
		"update": run(labelUpdateCmd, []string{"lbl_1"}),
		"delete": run(labelDeleteCmd, []string{"lbl_1"}),
	}
}

func TestLabelCmds_AuthGates(t *testing.T) {
	covSaveState(t)
	for name, invoke := range covLabelSubs(t) {
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

func TestLabelCmds_TransportErrors(t *testing.T) {
	covStubDown(t)
	for name, invoke := range covLabelSubs(t) {
		if err := invoke(); err == nil {
			t.Errorf("%s: expected transport error against dead server", name)
		}
	}
}

func TestLabelCmds_DecodeErrors(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/labels", clitest.TextResponse(200, "{nope"))
	s.OnPost("/api/v1/labels", clitest.TextResponse(200, "{nope"))
	subs := covLabelSubs(t)
	for _, name := range []string{"list", "create"} {
		if err := subs[name](); err == nil {
			t.Errorf("%s: expected decode error on malformed 200 body", name)
		}
	}
}

func TestLabelListRunE_RendersRows(t *testing.T) {
	s := covStubCli9(t)
	group := "priority"
	s.OnGet("/api/v1/labels", clitest.JSONResponse(200, []map[string]any{
		{"id": "clabelaaaaaaaaaaaaaaaa", "name": "urgent", "color": "#ff0000", "label_group": group},
		{"id": "clabelbbbbbbbbbbbbbbbb", "name": "later", "color": "#00ff00"},
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := labelListCmd.RunE(labelListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"urgent", "#ff0000", "later"} {
		if !strings.Contains(out, want) {
			t.Errorf("label table missing %q:\n%s", want, out)
		}
	}
}

func TestLabelCreateRunE_PostsBody(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/labels", clitest.JSONResponse(201, map[string]string{
		"id": "lbl_1", "name": "urgent", "color": "#ff0000",
	}))
	covSetFlagCli9(t, labelCreateCmd, "name", "urgent")
	covSetFlagCli9(t, labelCreateCmd, "color", "#ff0000")
	covSetFlagCli9(t, labelCreateCmd, "group", "priority")

	stderr := covCaptureStderrCli9(t, func() {
		if err := labelCreateCmd.RunE(labelCreateCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "Label created: urgent (lbl_1)") {
		t.Errorf("success line missing:\n%s", stderr)
	}

	calls := s.CallsFor("POST", "/api/v1/labels")
	if len(calls) != 1 {
		t.Fatalf("expected one POST, got %d", len(calls))
	}
	var body map[string]any
	_ = json.Unmarshal(calls[0].Body, &body)
	if body["name"] != "urgent" || body["color"] != "#ff0000" || body["label_group"] != "priority" {
		t.Errorf("create body = %v", body)
	}
}

func TestLabelUpdateRunE_PatchesChangedFields(t *testing.T) {
	s := covStubCli9(t)
	s.OnPatch("/api/v1/labels/lbl_1", clitest.JSONResponse(200, map[string]string{"id": "lbl_1"}))
	covSetFlagCli9(t, labelUpdateCmd, "name", "renamed")
	covSetFlagCli9(t, labelUpdateCmd, "color", "#abc")
	covSetFlagCli9(t, labelUpdateCmd, "group", "")

	stderr := covCaptureStderrCli9(t, func() {
		if err := labelUpdateCmd.RunE(labelUpdateCmd, []string{"lbl_1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "Label updated.") {
		t.Errorf("update confirmation missing:\n%s", stderr)
	}

	calls := s.CallsFor("PATCH", "/api/v1/labels/lbl_1")
	if len(calls) != 1 {
		t.Fatalf("expected one PATCH, got %d", len(calls))
	}
	var body map[string]any
	_ = json.Unmarshal(calls[0].Body, &body)
	if body["name"] != "renamed" || body["color"] != "#abc" {
		t.Errorf("update body = %v", body)
	}
	// Changed-but-empty group is sent as empty string (explicit clear).
	if v, ok := body["label_group"]; !ok || v != "" {
		t.Errorf("expected explicit empty label_group, body = %v", body)
	}
}

func TestLabelDeleteRunE_WithYes(t *testing.T) {
	s := covStubCli9(t)
	s.OnDelete("/api/v1/labels/lbl_1", clitest.EmptyResponse(204))
	covSetFlagCli9(t, labelDeleteCmd, "yes", "true")

	stderr := covCaptureStderrCli9(t, func() {
		if err := labelDeleteCmd.RunE(labelDeleteCmd, []string{"lbl_1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "Label deleted.") {
		t.Errorf("delete confirmation missing:\n%s", stderr)
	}
	if got := len(s.CallsFor("DELETE", "/api/v1/labels/lbl_1")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

func TestLabelDeleteRunE_AbortsWithoutYes(t *testing.T) {
	s := covStubCli9(t)
	covSetFlagCli9(t, labelDeleteCmd, "yes", "false")

	var err error
	_ = covCaptureStderrCli9(t, func() {
		err = labelDeleteCmd.RunE(labelDeleteCmd, []string{"lbl_1"})
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted; got %v", err)
	}
	if got := len(s.Calls()); got != 0 {
		t.Errorf("aborted delete must not hit the API (%d calls)", got)
	}
}

func TestLabelCmds_ServerErrors(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/labels", clitest.ErrorResponse(500, "labels down"))
	s.OnPost("/api/v1/labels", clitest.ErrorResponse(409, "duplicate name"))
	s.OnPatch("/api/v1/labels/x", clitest.ErrorResponse(404, "label missing"))
	s.OnDelete("/api/v1/labels/x", clitest.ErrorResponse(404, "label missing"))
	covSetFlagCli9(t, labelCreateCmd, "name", "n")
	covSetFlagCli9(t, labelCreateCmd, "color", "#abc")
	covSetFlagCli9(t, labelUpdateCmd, "name", "n2")
	covSetFlagCli9(t, labelDeleteCmd, "yes", "true")

	if err := labelListCmd.RunE(labelListCmd, nil); err == nil || !strings.Contains(err.Error(), "labels down") {
		t.Errorf("list: expected error; got %v", err)
	}
	if err := labelCreateCmd.RunE(labelCreateCmd, nil); err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("create: expected error; got %v", err)
	}
	if err := labelUpdateCmd.RunE(labelUpdateCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "label missing") {
		t.Errorf("update: expected error; got %v", err)
	}
	if err := labelDeleteCmd.RunE(labelDeleteCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "label missing") {
		t.Errorf("delete: expected error; got %v", err)
	}
}
