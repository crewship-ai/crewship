package main

// Coverage tests for cmd_agent_mcp.go — per-agent MCP binding CRUD.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

const covAgentIDCli7 = "cagnt1234567890123456789"

func covAgentStub(s *clitest.StubServer) {
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli7, "slug": "viktor"},
	}))
}

func TestShortBindingID(t *testing.T) {
	t.Parallel()
	if got := shortBindingID("short"); got != "short" {
		t.Errorf("short id mangled: %q", got)
	}
	if got := shortBindingID("abcdefghijkl"); got != "abcdefghijkl" {
		t.Errorf("12-char id should be untouched: %q", got)
	}
	if got := shortBindingID("abcdefghijklmnop"); got != "abcdefghijkl" {
		t.Errorf("long id should trim to 12: %q", got)
	}
}

func TestAgentMCPCmdStructure(t *testing.T) {
	t.Parallel()
	have := map[string]bool{}
	for _, sub := range agentMCPCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "add", "update", "remove"} {
		if !have[want] {
			t.Errorf("agent mcp missing subcommand %q", want)
		}
	}
}

func TestAgentMCP_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"list", agentMCPListCmd, []string{"viktor"}},
		{"add", agentMCPAddCmd, []string{"viktor", "wms_x"}},
		{"update", agentMCPUpdateCmd, []string{"viktor", "agm_x"}},
		{"remove", agentMCPRemoveCmd, []string{"viktor", "agm_x"}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", tc.name, err)
		}
	}
}

func TestAgentMCPListRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covAgentStub(s)

	credName := "jira-token"
	s.OnGet("/api/v1/agents/"+covAgentIDCli7+"/integrations", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "agm_aaaaaaaaaaaaaaaa", "agent_id": covAgentIDCli7,
			"mcp_server_id": "wms_1", "mcp_server_scope": "workspace",
			"credential_name": credName, "enabled": true,
			"server_name": "jira", "server_display_name": "Jira Cloud",
		},
		{
			"id": "agm_bbbbbbbbbbbbbbbb", "agent_id": covAgentIDCli7,
			"mcp_server_id": "cs_2", "mcp_server_scope": "crew",
			"enabled": false, "server_name": "github", "server_display_name": "",
		},
	}))

	out, err := covCaptureStdoutCli7(t, func() error {
		return agentMCPListCmd.RunE(agentMCPListCmd, []string{"viktor"})
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Truncated ids, display-name fallback, enabled mapping, credential dash.
	for _, want := range []string{"agm_aaaaaaaa", "Jira Cloud", "jira-token", "github", "workspace", "crew", "yes", "no"} {
		if !strings.Contains(out, want) {
			t.Errorf("list table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "agm_aaaaaaaaaaaaaaaa") {
		t.Errorf("full binding id should be trimmed in table:\n%s", out)
	}
}

func covResetAgentMCPAddFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = agentMCPAddCmd.Flags().Set("scope", "workspace")
		for _, f := range []string{"credential", "cred-type", "env-var"} {
			_ = agentMCPAddCmd.Flags().Set(f, "")
		}
		_ = agentMCPAddCmd.Flags().Set("enabled", "true")
		agentMCPAddCmd.Flags().Lookup("enabled").Changed = false
	})
}

func TestAgentMCPAddRunE(t *testing.T) {
	bindPath := "/api/v1/agents/" + covAgentIDCli7 + "/integrations"

	t.Run("invalid scope", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		covResetAgentMCPAddFlags(t)
		_ = agentMCPAddCmd.Flags().Set("scope", "global")
		err := agentMCPAddCmd.RunE(agentMCPAddCmd, []string{"viktor", "wms_1"})
		if err == nil || !strings.Contains(err.Error(), "--scope must be 'workspace' or 'crew'") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("full flag set lands in body", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnPost(bindPath, clitest.JSONResponse(201, map[string]any{"id": "agm_new", "enabled": false}))
		covResetAgentMCPAddFlags(t)
		_ = agentMCPAddCmd.Flags().Set("scope", "crew")
		_ = agentMCPAddCmd.Flags().Set("credential", "cred_xyz")
		_ = agentMCPAddCmd.Flags().Set("cred-type", "bearer")
		_ = agentMCPAddCmd.Flags().Set("env-var", "JIRA_TOKEN")
		_ = agentMCPAddCmd.Flags().Set("enabled", "false")

		if err := agentMCPAddCmd.RunE(agentMCPAddCmd, []string{"viktor", "cs_jira"}); err != nil {
			t.Fatalf("add: %v", err)
		}
		posts := s.CallsFor("POST", bindPath)
		if len(posts) != 1 {
			t.Fatalf("POSTs = %d", len(posts))
		}
		var body map[string]any
		_ = json.Unmarshal(posts[0].Body, &body)
		if body["mcp_server_id"] != "cs_jira" || body["mcp_server_scope"] != "crew" {
			t.Errorf("body = %v", body)
		}
		if body["credential_id"] != "cred_xyz" || body["cred_type"] != "bearer" || body["env_var_name"] != "JIRA_TOKEN" {
			t.Errorf("credential fields = %v", body)
		}
		if body["enabled"] != false {
			t.Errorf("enabled = %v, want explicit false", body["enabled"])
		}
	})

	t.Run("minimal add omits optional fields", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnPost(bindPath, clitest.JSONResponse(201, map[string]any{"id": "agm_new", "enabled": true}))
		covResetAgentMCPAddFlags(t)

		if err := agentMCPAddCmd.RunE(agentMCPAddCmd, []string{"viktor", "wms_1"}); err != nil {
			t.Fatalf("add: %v", err)
		}
		posts := s.CallsFor("POST", bindPath)
		var body map[string]any
		_ = json.Unmarshal(posts[len(posts)-1].Body, &body)
		for _, absent := range []string{"credential_id", "cred_type", "env_var_name", "enabled"} {
			if _, has := body[absent]; has {
				t.Errorf("%s should be omitted when not set: %v", absent, body)
			}
		}
	})

	t.Run("server error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnPost(bindPath, clitest.ErrorResponse(409, "binding exists"))
		covResetAgentMCPAddFlags(t)
		err := agentMCPAddCmd.RunE(agentMCPAddCmd, []string{"viktor", "wms_1"})
		if err == nil || !strings.Contains(err.Error(), "binding exists") {
			t.Fatalf("got %v", err)
		}
	})
}

func covResetAgentMCPUpdateFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, f := range []string{"credential", "cred-type", "env-var", "tools"} {
			_ = agentMCPUpdateCmd.Flags().Set(f, "")
			agentMCPUpdateCmd.Flags().Lookup(f).Changed = false
		}
		_ = agentMCPUpdateCmd.Flags().Set("enabled", "false")
		agentMCPUpdateCmd.Flags().Lookup("enabled").Changed = false
	})
}

func TestAgentMCPUpdateRunE(t *testing.T) {
	patchPath := "/api/v1/agents/" + covAgentIDCli7 + "/integrations/agm_x"

	t.Run("nothing to update", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		covResetAgentMCPUpdateFlags(t)
		err := agentMCPUpdateCmd.RunE(agentMCPUpdateCmd, []string{"viktor", "agm_x"})
		if err == nil || !strings.Contains(err.Error(), "nothing to update") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("tools list marshals into config_override_json", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{}))
		covResetAgentMCPUpdateFlags(t)
		_ = agentMCPUpdateCmd.Flags().Set("tools", " issue.create , issue.list ,, ")
		_ = agentMCPUpdateCmd.Flags().Set("enabled", "true")

		if err := agentMCPUpdateCmd.RunE(agentMCPUpdateCmd, []string{"viktor", "agm_x"}); err != nil {
			t.Fatalf("update: %v", err)
		}
		patches := s.CallsFor("PATCH", patchPath)
		if len(patches) != 1 {
			t.Fatalf("PATCHes = %d", len(patches))
		}
		var body map[string]any
		_ = json.Unmarshal(patches[0].Body, &body)
		if body["enabled"] != true {
			t.Errorf("enabled = %v", body["enabled"])
		}
		raw, _ := body["config_override_json"].(string)
		var override map[string][]string
		if err := json.Unmarshal([]byte(raw), &override); err != nil {
			t.Fatalf("config_override_json undecodable: %v (%q)", err, raw)
		}
		if len(override["tools"]) != 2 || override["tools"][0] != "issue.create" || override["tools"][1] != "issue.list" {
			t.Errorf("tools = %v, want trimmed two-entry allowlist", override["tools"])
		}
	})

	t.Run("empty tools clears override", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{}))
		covResetAgentMCPUpdateFlags(t)
		// Set then reset to "" so Changed stays true with an empty value.
		_ = agentMCPUpdateCmd.Flags().Set("tools", "")
		agentMCPUpdateCmd.Flags().Lookup("tools").Changed = true

		if err := agentMCPUpdateCmd.RunE(agentMCPUpdateCmd, []string{"viktor", "agm_x"}); err != nil {
			t.Fatalf("update: %v", err)
		}
		patches := s.CallsFor("PATCH", patchPath)
		var body map[string]any
		_ = json.Unmarshal(patches[len(patches)-1].Body, &body)
		if got, has := body["config_override_json"]; !has || got != "" {
			t.Errorf("config_override_json = %v, want empty string", got)
		}
	})

	t.Run("credential and env-var replacement", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnPatch(patchPath, clitest.JSONResponse(200, map[string]string{}))
		covResetAgentMCPUpdateFlags(t)
		_ = agentMCPUpdateCmd.Flags().Set("credential", "cred_new")
		_ = agentMCPUpdateCmd.Flags().Set("cred-type", "api_key")
		_ = agentMCPUpdateCmd.Flags().Set("env-var", "NEW_TOKEN")

		if err := agentMCPUpdateCmd.RunE(agentMCPUpdateCmd, []string{"viktor", "agm_x"}); err != nil {
			t.Fatalf("update: %v", err)
		}
		patches := s.CallsFor("PATCH", patchPath)
		var body map[string]any
		_ = json.Unmarshal(patches[len(patches)-1].Body, &body)
		if body["credential_id"] != "cred_new" || body["cred_type"] != "api_key" || body["env_var_name"] != "NEW_TOKEN" {
			t.Errorf("body = %v", body)
		}
		if _, has := body["enabled"]; has {
			t.Errorf("enabled not flagged Changed, must be absent: %v", body)
		}
	})

	t.Run("server error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnPatch(patchPath, clitest.ErrorResponse(404, "binding not found"))
		covResetAgentMCPUpdateFlags(t)
		_ = agentMCPUpdateCmd.Flags().Set("credential", "x")
		err := agentMCPUpdateCmd.RunE(agentMCPUpdateCmd, []string{"viktor", "agm_x"})
		if err == nil || !strings.Contains(err.Error(), "binding not found") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestAgentMCPRemoveRunE(t *testing.T) {
	delPath := "/api/v1/agents/" + covAgentIDCli7 + "/integrations/agm_x"

	t.Run("removes binding", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnDelete(delPath, clitest.EmptyResponse(204))

		if err := agentMCPRemoveCmd.RunE(agentMCPRemoveCmd, []string{"viktor", "agm_x"}); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if n := len(s.CallsFor("DELETE", delPath)); n != 1 {
			t.Errorf("DELETEs = %d", n)
		}
	})

	t.Run("unknown agent slug", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		err := agentMCPRemoveCmd.RunE(agentMCPRemoveCmd, []string{"ghost", "agm_x"})
		if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covAgentStub(s)
		s.OnDelete(delPath, clitest.ErrorResponse(500, "boom"))
		err := agentMCPRemoveCmd.RunE(agentMCPRemoveCmd, []string{"viktor", "agm_x"})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("got %v", err)
		}
	})
}
