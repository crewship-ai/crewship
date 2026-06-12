package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestTruncateImageRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mcr.microsoft.com/devcontainers/javascript-node:22-bookworm", "javascript-node:22-bookworm"},
		{"crewship-cache:02be226ac713abcd", "cache:02be226a"},
		{"crewship-cache:ab12", "cache:ab12"}, // short tag untouched
		{"ubuntu:24.04", "ubuntu:24.04"},      // no registry prefix
		{"ghcr.io/org/crewship-cache:0123456789abcdef", "cache:01234567"},
	}
	for _, tc := range cases {
		if got := truncateImageRef(tc.in); got != tc.want {
			t.Errorf("truncateImageRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStatusColor(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"COMPLETED", cli.Green},
		{"completed", cli.Green}, // case-insensitive
		{"RUNNING", cli.Blue},
		{"IN_PROGRESS", cli.Blue},
		{"PENDING", cli.Yellow},
		{"FAILED", cli.Red},
		{"ERROR", cli.Red},
		{"SOMETHING_ELSE", ""},
	}
	for _, tc := range cases {
		if got := statusColor(tc.in); got != tc.want {
			t.Errorf("statusColor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func covCrewListPayload() []map[string]any {
	return []map[string]any{
		{
			"id": covCrewIDCli4, "name": "Engineering", "slug": "engineering",
			"container_memory_mb": 2048, "container_cpus": 1.5,
			"network_mode": "restricted",
			"runtime_image": "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
			"cached_image":  "crewship-cache:02be226ac713abcd",
			"_count":        map[string]int{"agents": 3, "members": 2},
		},
		{
			"id": "ccrew20123456789abcdefgh", "name": "Marketing", "slug": "marketing",
			"container_memory_mb": 1024, "container_cpus": 1.0,
			"network_mode": "", // renders as "free"
			"_count":       map[string]int{"agents": 1, "members": 1},
		},
	}
}

func TestCrewListRunE_PlainTable(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, covCrewListPayload()))

	c := covFreshCmd(crewListCmd, func(c *cobra.Command) {
		c.Flags().Bool("runtime", false, "")
	})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"engineering", "2048MB", "1.5", "restricted", "marketing", "free"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "RUNTIME") {
		t.Errorf("runtime columns shown without --runtime: %q", out)
	}
}

func TestCrewListRunE_RuntimeColumns(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, covCrewListPayload()))

	c := covFreshCmd(crewListCmd, func(c *cobra.Command) {
		c.Flags().Bool("runtime", false, "")
	})
	covSetFlagsCli4(t, c, map[string]string{"runtime": "true"})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"RUNTIME", "CACHED", "PROVISIONED", "javascript-node:22-bookworm", "cache:02be226a", "yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("runtime output missing %q: %q", want, out)
		}
	}
	// Marketing has no images → em-dash placeholders + provisioned "no".
	if !strings.Contains(out, "—") {
		t.Errorf("placeholder for missing image missing: %q", out)
	}
}

func TestCrewGetRunE_DetailPairs(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli4, "slug": "engineering"},
	}))
	ttl := 6
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli4, "name": "Engineering", "slug": "engineering",
		"description": "builders", "container_memory_mb": 2048, "container_cpus": 2.0,
		"container_ttl_hours": ttl, "network_mode": "restricted",
		"allowed_domains": []string{"github.com", "npmjs.com"},
		"created_at":      "2026-05-01T00:00:00Z",
	}))

	c := covFreshCmd(crewGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"engineering"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Engineering", "builders", "2048MB", "2.0", "6 hours", "restricted", "github.com, npmjs.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("crew detail missing %q: %q", want, out)
		}
	}
}

func TestCrewGetRunE_DefaultsForNullables(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]any{
		"id": covCrewIDCli4, "name": "Bare", "slug": "bare",
		"container_memory_mb": 512, "container_cpus": 0.5,
		"created_at": "2026-05-01T00:00:00Z",
	}))

	c := covFreshCmd(crewGetCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Never stop") {
		t.Errorf("nil TTL should render 'Never stop': %q", out)
	}
	if !strings.Contains(out, "free") {
		t.Errorf("empty network mode should render 'free': %q", out)
	}
}

func TestCrewStatusRunE_CompoundView(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli4, "slug": "engineering"},
	}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]string{
		"name": "Engineering", "slug": "engineering",
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"slug": "viktor", "agent_role": "LEAD", "status": "ACTIVE"},
		{"slug": "eva", "agent_role": "AGENT", "status": "IDLE"},
	}))
	longTask := strings.Repeat("refactor the orchestrator ", 5) // > 60 runes → truncated
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/assignments", clitest.JSONResponse(200, []map[string]any{
		{"task": longTask, "status": "RUNNING", "assigned_by_slug": "viktor", "assigned_to_slug": "eva"},
		{"task": "short one", "status": "COMPLETED", "assigned_by_slug": "viktor", "assigned_to_slug": nil},
	}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/escalations", clitest.JSONResponse(200, []map[string]string{
		{"reason": "needs human approval", "status": "PENDING"},
		{"reason": "already handled", "status": "RESOLVED"},
	}))

	c := covFreshCmd(crewStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"engineering"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Crew: ") || !strings.Contains(out, "(engineering)") {
		t.Errorf("header missing: %q", out)
	}
	if !strings.Contains(out, "AGENTS (2):") || !strings.Contains(out, "viktor") || !strings.Contains(out, "eva") {
		t.Errorf("agents section missing: %q", out)
	}
	if !strings.Contains(out, "ASSIGNMENTS (last 5):") || !strings.Contains(out, "...") {
		t.Errorf("assignments truncation missing: %q", out)
	}
	if !strings.Contains(out, "viktor -> -:") {
		t.Errorf("nil assignee should render '-': %q", out)
	}
	if !strings.Contains(out, "needs human approval") {
		t.Errorf("open escalation missing: %q", out)
	}
	if strings.Contains(out, "already handled") {
		t.Errorf("resolved escalation should be hidden: %q", out)
	}
}

func TestCrewStatusRunE_EmptySections(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]string{
		"name": "Empty", "slug": "empty",
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/assignments", clitest.JSONResponse(200, []map[string]any{}))
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/escalations", clitest.JSONResponse(200, []map[string]string{}))

	c := covFreshCmd(crewStatusCmd, nil)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"No agents", "No assignments", "None"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-state %q missing: %q", want, out)
		}
	}
}

func TestCrewStatusRunE_AgentsFetchError(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]string{
		"name": "Engineering", "slug": "engineering",
	}))
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "boom"))

	c := covFreshCmd(crewStatusCmd, nil)
	_, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{covCrewIDCli4}) })
	if err == nil || !strings.Contains(err.Error(), "fetch agents") {
		t.Fatalf("want fetch-agents error, got %v", err)
	}
}

// ─── remaining error branches ────────────────────────────────────────

func TestCrewListGetStatusGates_AuthAndWorkspace(t *testing.T) {
	builders := map[string]func() *cobra.Command{
		"list": func() *cobra.Command {
			return covFreshCmd(crewListCmd, func(c *cobra.Command) { c.Flags().Bool("runtime", false, "") })
		},
		"get":    func() *cobra.Command { return covFreshCmd(crewGetCmd, nil) },
		"status": func() *cobra.Command { return covFreshCmd(crewStatusCmd, nil) },
	}
	for name, build := range builders {
		t.Run(name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			c := build()
			if err := c.RunE(c, []string{covCrewIDCli4}); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("want not-logged-in, got %v", err)
			}
		})
		t.Run(name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tok"}
			c := build()
			if err := c.RunE(c, []string{covCrewIDCli4}); err == nil || !strings.Contains(err.Error(), "no workspace set") {
				t.Errorf("want workspace error, got %v", err)
			}
		})
	}
}

func TestCrewListRunE_TransportServerDecodeErrors(t *testing.T) {
	c := covFreshCmd(crewListCmd, func(c *cobra.Command) { c.Flags().Bool("runtime", false, "") })

	covDeadServerCli4(t)
	if err := c.RunE(c, nil); err == nil {
		t.Error("want transport error")
	}

	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.ErrorResponse(500, "crews exploded"))
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "crews exploded") {
		t.Errorf("want server error, got %v", err)
	}

	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, map[string]string{"not": "array"}))
	if err := c.RunE(c, nil); err == nil {
		t.Error("want decode error")
	}
}

func TestCrewGetRunE_ErrorBranches(t *testing.T) {
	c := covFreshCmd(crewGetCmd, nil)

	// Slug arg → resolve fails against the dead server.
	covDeadServerCli4(t)
	if err := c.RunE(c, []string{"engineering"}); err == nil || !strings.Contains(err.Error(), "resolve crew") {
		t.Errorf("want resolve error, got %v", err)
	}
	// CUID arg skips resolution → the detail GET fails.
	if err := c.RunE(c, []string{covCrewIDCli4}); err == nil {
		t.Error("want transport error on detail fetch")
	}

	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.ErrorResponse(404, "crew gone"))
	if err := c.RunE(c, []string{covCrewIDCli4}); err == nil || !strings.Contains(err.Error(), "crew gone") {
		t.Errorf("want server error, got %v", err)
	}

	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.TextResponse(200, "not-json"))
	if err := c.RunE(c, []string{covCrewIDCli4}); err == nil {
		t.Error("want decode error")
	}
}

func TestCrewStatusRunE_PerEndpointFailures(t *testing.T) {
	// Each sub-fetch failure surfaces with its own prefix so the
	// operator knows which call broke.
	type tc struct {
		name     string
		install  func(stub *clitest.StubServer)
		wantErr  string
	}
	healthyCrew := func(stub *clitest.StubServer) {
		stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, map[string]string{"name": "E", "slug": "e"}))
	}
	healthyAgents := func(stub *clitest.StubServer) {
		stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	}
	healthyAssignments := func(stub *clitest.StubServer) {
		stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/assignments", clitest.JSONResponse(200, []map[string]any{}))
	}
	cases := []tc{
		{"crew detail 500", func(stub *clitest.StubServer) {
			stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.ErrorResponse(500, "detail boom"))
		}, "detail boom"},
		{"crew detail malformed", func(stub *clitest.StubServer) {
			stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.TextResponse(200, "not-json"))
		}, ""},
		{"agents malformed", func(stub *clitest.StubServer) {
			healthyCrew(stub)
			stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, map[string]string{"x": "y"}))
		}, "parse agents"},
		{"assignments 500", func(stub *clitest.StubServer) {
			healthyCrew(stub)
			healthyAgents(stub)
			stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/assignments", clitest.ErrorResponse(500, "boom"))
		}, "fetch assignments"},
		{"assignments malformed", func(stub *clitest.StubServer) {
			healthyCrew(stub)
			healthyAgents(stub)
			stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/assignments", clitest.JSONResponse(200, map[string]string{"x": "y"}))
		}, "parse assignments"},
		{"escalations 500", func(stub *clitest.StubServer) {
			healthyCrew(stub)
			healthyAgents(stub)
			healthyAssignments(stub)
			stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/escalations", clitest.ErrorResponse(500, "boom"))
		}, "fetch escalations"},
		{"escalations malformed", func(stub *clitest.StubServer) {
			healthyCrew(stub)
			healthyAgents(stub)
			healthyAssignments(stub)
			stub.OnGet("/api/v1/crews/"+covCrewIDCli4+"/escalations", clitest.JSONResponse(200, map[string]string{"x": "y"}))
		}, "parse escalations"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := covSetupCli4(t)
			c.install(stub)
			cmd := covFreshCmd(crewStatusCmd, nil)
			_, err := covCaptureStdoutCli4(t, func() error { return cmd.RunE(cmd, []string{covCrewIDCli4}) })
			if err == nil {
				t.Fatal("want error")
			}
			if c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %v, want contains %q", err, c.wantErr)
			}
		})
	}
}

func TestCrewStatusRunE_ResolveErrorViaSlug(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	c := covFreshCmd(crewStatusCmd, nil)
	if err := c.RunE(c, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Fatalf("want resolve error, got %v", err)
	}
}
