package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// stubAgentDirectory registers the agent list used by slug→ID
// resolution in every introspection command.
func stubAgentDirectory(stub *clitest.StubServer) {
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli4, "slug": "viktor"},
	}))
}

func TestAgentRunsRunE_HappyPath(t *testing.T) {
	stub := covSetupCli4(t)
	stubAgentDirectory(stub)
	finished := "2026-06-01T10:00:00Z"
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/runs", clitest.JSONResponse(200, []map[string]any{
		{"id": "run-1", "status": "COMPLETED", "trigger_type": "chat", "created_at": "2026-06-01T09:00:00Z", "finished_at": finished},
		{"id": "run-2", "status": "RUNNING", "trigger_type": "schedule", "created_at": "2026-06-01T11:00:00Z", "finished_at": nil},
	}))

	c := covFreshCmd(agentRunsCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"viktor"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "run-1") || !strings.Contains(out, "COMPLETED") || !strings.Contains(out, finished) {
		t.Errorf("finished run row missing: %q", out)
	}
	// Unfinished run renders "-" for FINISHED.
	if !strings.Contains(out, "run-2") || !strings.Contains(out, "RUNNING") {
		t.Errorf("running row missing: %q", out)
	}
	if got := len(stub.CallsFor("GET", "/api/v1/agents/"+covAgentIDCli4+"/runs")); got != 1 {
		t.Errorf("runs endpoint calls = %d, want 1", got)
	}
}

func TestAgentRunsRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	c := covFreshCmd(agentRunsCmd, nil)
	err := c.RunE(c, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want not-logged-in, got %v", err)
	}
}

func TestAgentRunsRunE_UnknownSlugSuggests(t *testing.T) {
	stub := covSetupCli4(t)
	stubAgentDirectory(stub)
	c := covFreshCmd(agentRunsCmd, nil)
	err := c.RunE(c, []string{"vitkor"})
	if err == nil || !strings.Contains(err.Error(), "agent not found: vitkor") {
		t.Fatalf("want agent-not-found, got %v", err)
	}
	if !strings.Contains(err.Error(), "viktor") {
		t.Errorf("expected near-match suggestion 'viktor' in: %v", err)
	}
}

func TestAgentStopRunE_PostsStop(t *testing.T) {
	stub := covSetupCli4(t)
	stubAgentDirectory(stub)
	stub.OnPost("/api/v1/agents/"+covAgentIDCli4+"/stop", clitest.EmptyResponse(200))

	c := covFreshCmd(agentStopCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"viktor"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Agent viktor stopped.") {
		t.Errorf("success line missing: %q", out)
	}
	if got := len(stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli4+"/stop")); got != 1 {
		t.Errorf("stop calls = %d, want 1", got)
	}
}

func TestAgentLogsRunE_TailQueryAndRawPrint(t *testing.T) {
	stub := covSetupCli4(t)
	stubAgentDirectory(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/logs", clitest.JSONResponse(200, map[string]string{
		"logs": "line-a\nline-b\n",
	}))

	c := covFreshCmd(agentLogsCmd, func(c *cobra.Command) {
		c.Flags().Int("tail", 0, "")
	})
	covSetFlagsCli4(t, c, map[string]string{"tail": "25"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"viktor"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "line-a\nline-b\n") {
		t.Errorf("raw logs not printed: %q", out)
	}

	calls := stub.CallsFor("GET", "/api/v1/agents/"+covAgentIDCli4+"/logs")
	if len(calls) != 1 {
		t.Fatalf("logs calls = %d, want 1", len(calls))
	}
	if !strings.Contains(calls[0].Query, "tail=25") {
		t.Errorf("tail not propagated, query=%q", calls[0].Query)
	}
}

func TestAgentLogsRunE_NoLogsField(t *testing.T) {
	stub := covSetupCli4(t)
	stubAgentDirectory(stub)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/logs", clitest.JSONResponse(200, map[string]any{}))

	c := covFreshCmd(agentLogsCmd, func(c *cobra.Command) {
		c.Flags().Int("tail", 0, "")
	})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covAgentIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "No logs available.") {
		t.Errorf("expected fallback message, got %q", out)
	}
}

func TestAgentDebugRunE_EmitsJSON(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/debug", clitest.JSONResponse(200, map[string]any{
		"container_state": "running",
		"crewshipd":       "healthy",
	}))

	c := covFreshCmd(agentDebugCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covAgentIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"container_state": "running"`) {
		t.Errorf("debug JSON missing: %q", out)
	}
}

func TestAgentSkillsRunE_EmptyAndPopulated(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/skills", clitest.JSONResponse(200, []map[string]any{}))

	c := covFreshCmd(agentSkillsCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covAgentIDCli4}) })
	if err != nil {
		t.Fatalf("RunE empty: %v", err)
	}
	if !strings.Contains(out, "No skills assigned to this agent.") {
		t.Errorf("empty-state line missing: %q", out)
	}

	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/skills", clitest.JSONResponse(200, []map[string]any{
		{"id": "as-1", "skill_id": "skill0123456789xyz", "skill_name": "code-review", "category": "engineering", "enabled": true},
		{"id": "as-2", "skill_id": "skill-2", "skill_name": "deploys", "category": "ops", "enabled": false},
	}))
	out, err = covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covAgentIDCli4}) })
	if err != nil {
		t.Fatalf("RunE populated: %v", err)
	}
	// skill_id truncated to 12 chars in the table.
	if !strings.Contains(out, "skill0123456") || !strings.Contains(out, "code-review") {
		t.Errorf("populated row missing: %q", out)
	}
	if !strings.Contains(out, "yes") || !strings.Contains(out, "no") {
		t.Errorf("enabled yes/no rendering missing: %q", out)
	}
}

func TestAgentChatsRunE_HappyPath(t *testing.T) {
	stub := covSetupCli4(t)
	stubAgentDirectory(stub)
	title := "Sprint planning"
	ended := "2026-06-02T12:00:00Z"
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/chats", clitest.JSONResponse(200, []map[string]any{
		{"id": "chat-1", "title": title, "status": "ACTIVE", "message_count": 7, "started_at": "2026-06-02T09:00:00Z", "ended_at": nil},
		{"id": "chat-2", "title": nil, "status": "ENDED", "message_count": 2, "started_at": "2026-06-01T09:00:00Z", "ended_at": ended},
	}))

	c := covFreshCmd(agentChatsCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"viktor"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "chat-1") || !strings.Contains(out, title) || !strings.Contains(out, "ACTIVE") {
		t.Errorf("chat row missing: %q", out)
	}
	if !strings.Contains(out, "chat-2") || !strings.Contains(out, ended) {
		t.Errorf("ended chat row missing: %q", out)
	}
}

func TestAgentChatsRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok"}
	c := covFreshCmd(agentChatsCmd, nil)
	err := c.RunE(c, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "no workspace set") {
		t.Fatalf("want workspace error, got %v", err)
	}
}

func TestAgentCredentialsRunE_EmptyAndPopulated(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/credentials", clitest.JSONResponse(200, []map[string]any{}))

	c := covFreshCmd(agentCredentialsCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covAgentIDCli4}) })
	if err != nil {
		t.Fatalf("RunE empty: %v", err)
	}
	if !strings.Contains(out, "No credentials assigned to this agent.") {
		t.Errorf("empty-state line missing: %q", out)
	}

	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/credentials", clitest.JSONResponse(200, []map[string]any{
		{"id": "assignment012345", "credential_id": "cred-1", "credential_name": "GitHub PAT", "provider": "github", "type": "SECRET", "env_var_name": "GITHUB_TOKEN"},
	}))
	out, err = covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covAgentIDCli4}) })
	if err != nil {
		t.Fatalf("RunE populated: %v", err)
	}
	// id truncated to 12 chars.
	if !strings.Contains(out, "assignment01") || !strings.Contains(out, "GitHub PAT") || !strings.Contains(out, "GITHUB_TOKEN") {
		t.Errorf("credential row missing: %q", out)
	}
}

func TestAgentIntrospect_ServerErrorSurfaces(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/debug", clitest.ErrorResponse(403, "debug requires admin"))
	c := covFreshCmd(agentDebugCmd, nil)
	err := c.RunE(c, []string{covAgentIDCli4})
	if err == nil || !strings.Contains(err.Error(), "debug requires admin") {
		t.Fatalf("want server error surfaced, got %v", err)
	}
}

// ─── remaining error branches ────────────────────────────────────────

// covIntrospectCmds builds fresh instances of every per-agent
// introspection command, keyed by the endpoint suffix each one hits.
func covIntrospectCmds() map[string]*cobra.Command {
	return map[string]*cobra.Command{
		"runs":        covFreshCmd(agentRunsCmd, nil),
		"stop":        covFreshCmd(agentStopCmd, nil),
		"logs":        covFreshCmd(agentLogsCmd, func(c *cobra.Command) { c.Flags().Int("tail", 0, "") }),
		"debug":       covFreshCmd(agentDebugCmd, nil),
		"skills":      covFreshCmd(agentSkillsCmd, nil),
		"chats":       covFreshCmd(agentChatsCmd, nil),
		"credentials": covFreshCmd(agentCredentialsCmd, nil),
	}
}

func TestAgentIntrospectGates_AuthAndWorkspace(t *testing.T) {
	for name, c := range covIntrospectCmds() {
		t.Run(name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := c.RunE(c, []string{"viktor"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("want not-logged-in, got %v", err)
			}
		})
		t.Run(name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tok"}
			if err := c.RunE(c, []string{"viktor"}); err == nil || !strings.Contains(err.Error(), "no workspace set") {
				t.Errorf("want workspace error, got %v", err)
			}
		})
	}
}

func TestAgentIntrospect_ResolveFailurePerCommand(t *testing.T) {
	for name, c := range covIntrospectCmds() {
		t.Run(name, func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
			err := c.RunE(c, []string{"ghost"})
			if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
				t.Errorf("want resolve failure, got %v", err)
			}
		})
	}
}

func TestAgentIntrospect_EndpointErrorPerCommand(t *testing.T) {
	endpoints := map[string]struct {
		method, path string
	}{
		"runs":        {"GET", "/api/v1/agents/" + covAgentIDCli4 + "/runs"},
		"stop":        {"POST", "/api/v1/agents/" + covAgentIDCli4 + "/stop"},
		"logs":        {"GET", "/api/v1/agents/" + covAgentIDCli4 + "/logs"},
		"skills":      {"GET", "/api/v1/agents/" + covAgentIDCli4 + "/skills"},
		"chats":       {"GET", "/api/v1/agents/" + covAgentIDCli4 + "/chats"},
		"credentials": {"GET", "/api/v1/agents/" + covAgentIDCli4 + "/credentials"},
	}
	cmds := covIntrospectCmds()
	for name, ep := range endpoints {
		t.Run(name, func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.On(ep.method, ep.path, clitest.ErrorResponse(500, "introspect exploded"))
			c := cmds[name]
			err := c.RunE(c, []string{covAgentIDCli4})
			if err == nil || !strings.Contains(err.Error(), "introspect exploded") {
				t.Errorf("want endpoint error surfaced, got %v", err)
			}
		})
	}
}

func TestAgentIntrospect_MalformedResponsePerCommand(t *testing.T) {
	// Object where an array is expected (and vice versa) breaks ReadJSON.
	paths := map[string]string{
		"runs":        "/api/v1/agents/" + covAgentIDCli4 + "/runs",
		"skills":      "/api/v1/agents/" + covAgentIDCli4 + "/skills",
		"chats":       "/api/v1/agents/" + covAgentIDCli4 + "/chats",
		"credentials": "/api/v1/agents/" + covAgentIDCli4 + "/credentials",
	}
	cmds := covIntrospectCmds()
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.OnGet(path, clitest.JSONResponse(200, map[string]string{"not": "an array"}))
			c := cmds[name]
			if err := c.RunE(c, []string{covAgentIDCli4}); err == nil {
				t.Error("want decode error for object-instead-of-array response")
			}
		})
	}

	// logs + debug decode into maps; non-JSON breaks them.
	for _, name := range []string{"logs", "debug"} {
		t.Run(name, func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.OnGet("/api/v1/agents/"+covAgentIDCli4+"/"+name, clitest.TextResponse(200, "not-json"))
			c := cmds[name]
			if err := c.RunE(c, []string{covAgentIDCli4}); err == nil {
				t.Error("want decode error for non-JSON response")
			}
		})
	}
}

func TestAgentIntrospect_TransportErrorPerCommand(t *testing.T) {
	for name, c := range covIntrospectCmds() {
		t.Run(name, func(t *testing.T) {
			covDeadServerCli4(t)
			if err := c.RunE(c, []string{covAgentIDCli4}); err == nil {
				t.Error("want transport error against dead server")
			}
		})
	}
}
