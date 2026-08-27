package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestIntgToolsList_RendersBindings(t *testing.T) {
	s := covStubCli9(t)
	longDesc := strings.Repeat("x", 60)
	s.OnGet("/api/v1/crews/"+covCrew+"/integrations/intg1/tools", clitest.JSONResponse(200, []map[string]any{
		{"id": "b1", "tool_name": "list_issues", "description": longDesc, "enabled": true, "updated_at": "2026-06-01"},
		{"id": "b2", "tool_name": "create_pr", "description": nil, "enabled": false, "updated_at": "2026-06-02"},
	}))

	out := covCaptureStdoutCli9(t, func() {
		if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"list_issues", "create_pr", strings.Repeat("x", 47) + "..."} {
		if !strings.Contains(out, want) {
			t.Errorf("tools table missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, longDesc) {
		t.Errorf("60-char description should be truncated:\n%s", out)
	}
}

func TestToggleCrewIntegrationTool_EnableDisable(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disable"
		cmd := intgToolsDisableCmd
		if enabled {
			name = "enable"
			cmd = intgToolsEnableCmd
		}
		t.Run(name, func(t *testing.T) {
			s := covStubCli9(t)
			// Tool name with a space exercises the PathEscape branch:
			// the server sees the decoded path segment.
			s.OnPatch("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/my tool", clitest.JSONResponse(200, map[string]bool{"ok": true}))

			out := covCaptureStdoutCli9(t, func() {
				if err := cmd.RunE(cmd, []string{covCrew, "intg1", "my tool"}); err != nil {
					t.Errorf("RunE: %v", err)
				}
			})
			wantState := "disabled."
			if enabled {
				wantState = "enabled."
			}
			if !strings.Contains(out, "Tool my tool on "+covCrew+"/intg1 "+wantState) {
				t.Errorf("confirmation missing:\n%s", out)
			}

			calls := s.CallsFor("PATCH", "/api/v1/crews/"+covCrew+"/integrations/intg1/tools/my tool")
			if len(calls) != 1 {
				t.Fatalf("expected one PATCH, got %d", len(calls))
			}
			var body map[string]bool
			_ = json.Unmarshal(calls[0].Body, &body)
			if body["enabled"] != enabled {
				t.Errorf("PATCH body = %v, want enabled=%v", body, enabled)
			}
		})
	}
}

func TestToggleCrewIntegrationTool_ResolvesCrewSlug(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrewresolved12345678", "slug": "backend"},
	}))
	s.OnPatch("/api/v1/crews/ccrewresolved12345678/integrations/intg1/tools/t1", clitest.JSONResponse(200, map[string]bool{"ok": true}))

	_ = covCaptureStdoutCli9(t, func() {
		if err := toggleCrewIntegrationTool("backend", "intg1", "t1", true); err != nil {
			t.Errorf("toggle: %v", err)
		}
	})
	if got := len(s.CallsFor("PATCH", "/api/v1/crews/ccrewresolved12345678/integrations/intg1/tools/t1")); got != 1 {
		t.Errorf("PATCH after slug resolution = %d calls, want 1", got)
	}
}

func TestToggleCrewIntegrationTool_Errors(t *testing.T) {
	t.Run("crew not found", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
		err := toggleCrewIntegrationTool("ghost-crew", "intg1", "t1", true)
		if err == nil || !strings.Contains(err.Error(), "crew not found: ghost-crew") {
			t.Errorf("expected crew-not-found; got %v", err)
		}
	})
	t.Run("server rejects patch", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPatch("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/t1", clitest.ErrorResponse(404, "binding missing"))
		err := toggleCrewIntegrationTool(covCrew, "intg1", "t1", false)
		if err == nil || !strings.Contains(err.Error(), "binding missing") {
			t.Errorf("expected binding error; got %v", err)
		}
	})
	t.Run("no auth", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{}
		if err := toggleCrewIntegrationTool("c", "i", "t", true); err == nil {
			t.Error("expected not-logged-in error")
		}
	})
	t.Run("no workspace", func(t *testing.T) {
		covSaveState(t)
		cliCfg = &cli.CLIConfig{Token: "tok"}
		err := toggleCrewIntegrationTool("c", "i", "t", true)
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("expected workspace error; got %v", err)
		}
	})
}

// covRefreshTools arms `tools refresh` with the discovered tools a real
// caller would pass, and guarantees the repeatable flag is emptied again
// afterwards (the command object is a package global).
func covRefreshTools(t *testing.T, tools ...string) {
	t.Helper()
	covResetFlags(t, intgToolsRefreshCmd)
	for _, tool := range tools {
		if err := intgToolsRefreshCmd.Flags().Set("tool", tool); err != nil {
			t.Fatalf("set --tool=%s: %v", tool, err)
		}
	}
}

// covRefreshBody decodes the single POST the refresh command made.
func covRefreshBody(t *testing.T, s *clitest.StubServer) map[string]any {
	t.Helper()
	calls := s.CallsFor("POST", "/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh")
	if len(calls) != 1 {
		t.Fatalf("expected exactly one refresh POST, got %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("refresh body is not JSON (%v): %s", err, calls[0].Body)
	}
	return body
}

// The point of the command: the tools the caller discovered reach the wire.
// This replaces an older assertion that pinned the defect in #1884 — it
// required the body to be `"tools":[]`, i.e. it specified the no-op.
func TestIntgToolsRefresh_SendsDiscoveredTools(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))
	covRefreshTools(t, "search=Full-text search over the issue tracker", "create_issue")

	out := covCaptureStdoutCli9(t, func() {
		if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Tool bindings refresh requested for "+covCrew+"/intg1.") {
		t.Errorf("refresh confirmation missing:\n%s", out)
	}

	body := covRefreshBody(t, s)
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("tools = %#v, want the 2 discovered entries", body["tools"])
	}
	first, _ := tools[0].(map[string]any)
	if first["name"] != "search" {
		t.Errorf("first tool name = %v, want search", first["name"])
	}
	if first["description"] != "Full-text search over the issue tracker" {
		t.Errorf("first tool description = %v, want the text after '='", first["description"])
	}
	second, _ := tools[1].(map[string]any)
	if second["name"] != "create_issue" {
		t.Errorf("second tool name = %v, want create_issue", second["name"])
	}
	// A bare --tool must omit description entirely: the server COALESCEs it,
	// so sending an explicit null would still be safe, but omitting it is the
	// honest encoding of "I did not discover a description".
	if _, present := second["description"]; present {
		t.Errorf("bare --tool must omit description, got %#v", second)
	}
}

// `--tool name=desc` splits on the FIRST '=' only, so a description that
// contains '=' survives intact.
func TestIntgToolsRefresh_DescriptionMayContainEquals(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))
	covRefreshTools(t, "query=Run SQL, e.g. a=b")

	_ = covCaptureStdoutCli9(t, func() {
		if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	tools, _ := covRefreshBody(t, s)["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want 1", tools)
	}
	entry, _ := tools[0].(map[string]any)
	if entry["description"] != "Run SQL, e.g. a=b" {
		t.Errorf("description = %v, want everything after the first '='", entry["description"])
	}
}

// Refusing to run with no tools is the whole fix: silently posting an empty
// list is a no-op that prints a success line, which is how #1884 hid.
func TestIntgToolsRefresh_RequiresTools(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))
	covResetFlags(t, intgToolsRefreshCmd)

	err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "--tool") {
		t.Fatalf("expected a validation error naming --tool; got %v", err)
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitValidation {
		t.Errorf("exit code = %d, want ExitValidation (%d)", code, cli.ExitValidation)
	}
	if got := len(s.CallsFor("POST", "/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh")); got != 0 {
		t.Errorf("a rejected refresh must not touch the server; got %d POSTs", got)
	}
}

// An explicitly supplied empty array is still allowed: a probe that
// legitimately found no tools is a deliberate no-op, unlike "user forgot".
func TestIntgToolsRefresh_ExplicitEmptyFileIsAllowed(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))
	covResetFlags(t, intgToolsRefreshCmd)
	path := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := intgToolsRefreshCmd.Flags().Set("tools-file", path); err != nil {
		t.Fatalf("set --tools-file: %v", err)
	}

	_ = covCaptureStdoutCli9(t, func() {
		if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	body := covRefreshBody(t, s)
	if tools, ok := body["tools"].([]any); !ok || len(tools) != 0 {
		t.Errorf("tools = %#v, want an explicit empty array", body["tools"])
	}
}

func TestIntgToolsRefresh_ToolsFile(t *testing.T) {
	t.Run("bare array", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))
		covResetFlags(t, intgToolsRefreshCmd)
		path := filepath.Join(t.TempDir(), "tools.json")
		if err := os.WriteFile(path, []byte(`[{"name":"search","description":"find things"},{"name":"ping"}]`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := intgToolsRefreshCmd.Flags().Set("tools-file", path); err != nil {
			t.Fatalf("set --tools-file: %v", err)
		}

		_ = covCaptureStdoutCli9(t, func() {
			if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		tools, _ := covRefreshBody(t, s)["tools"].([]any)
		if len(tools) != 2 {
			t.Fatalf("tools = %#v, want 2 from the file", tools)
		}
	})

	t.Run("mcp tools/list envelope", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))
		covResetFlags(t, intgToolsRefreshCmd)
		path := filepath.Join(t.TempDir(), "tools.json")
		// Exactly what an MCP `tools/list` result looks like, extra keys and all.
		raw := `{"tools":[{"name":"search","description":"find things","inputSchema":{"type":"object"}}]}`
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := intgToolsRefreshCmd.Flags().Set("tools-file", path); err != nil {
			t.Fatalf("set --tools-file: %v", err)
		}

		_ = covCaptureStdoutCli9(t, func() {
			if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		tools, _ := covRefreshBody(t, s)["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tools = %#v, want the single entry unwrapped from the envelope", tools)
		}
		entry, _ := tools[0].(map[string]any)
		if entry["name"] != "search" || entry["description"] != "find things" {
			t.Errorf("entry = %#v, want name/description only", entry)
		}
		if _, extra := entry["inputSchema"]; extra {
			t.Errorf("inputSchema must not be forwarded; the endpoint has no column for it: %#v", entry)
		}
	})

	t.Run("later --tool wins over the file", func(t *testing.T) {
		s := covStubCli9(t)
		s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.EmptyResponse(200))
		covResetFlags(t, intgToolsRefreshCmd)
		path := filepath.Join(t.TempDir(), "tools.json")
		if err := os.WriteFile(path, []byte(`[{"name":"search","description":"stale"}]`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := intgToolsRefreshCmd.Flags().Set("tools-file", path); err != nil {
			t.Fatalf("set --tools-file: %v", err)
		}
		if err := intgToolsRefreshCmd.Flags().Set("tool", "search=fresh"); err != nil {
			t.Fatalf("set --tool: %v", err)
		}

		_ = covCaptureStdoutCli9(t, func() {
			if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		tools, _ := covRefreshBody(t, s)["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tools = %#v, want the duplicate collapsed to one entry", tools)
		}
		entry, _ := tools[0].(map[string]any)
		if entry["description"] != "fresh" {
			t.Errorf("description = %v, want the --tool value to win", entry["description"])
		}
	})

	t.Run("errors", func(t *testing.T) {
		dir := t.TempDir()
		badJSON := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(badJSON, []byte(`{nope`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		unnamed := filepath.Join(dir, "unnamed.json")
		if err := os.WriteFile(unnamed, []byte(`[{"description":"no name"}]`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		for _, tc := range []struct{ name, file, want string }{
			{"missing file", filepath.Join(dir, "nope.json"), "read --tools-file"},
			{"malformed json", badJSON, "--tools-file"},
			{"entry without a name", unnamed, "name"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				covStubCli9(t)
				covResetFlags(t, intgToolsRefreshCmd)
				if err := intgToolsRefreshCmd.Flags().Set("tools-file", tc.file); err != nil {
					t.Fatalf("set --tools-file: %v", err)
				}
				err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected an error containing %q; got %v", tc.want, err)
				}
			})
		}
	})
}

// --tool with an empty name is a typo, not a payload.
func TestIntgToolsRefresh_RejectsBlankToolName(t *testing.T) {
	covStubCli9(t)
	covRefreshTools(t, "=just a description")
	err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "--tool") {
		t.Fatalf("expected a --tool validation error; got %v", err)
	}
}

func TestIntgToolsRefresh_JSONResult(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.JSONResponse(200, map[string]any{
		"upserted": 3, "kept": 1,
	}))
	covRefreshTools(t, "search")

	out := covCaptureStdoutCli9(t, func() {
		if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"upserted"`) {
		t.Errorf("refresh result JSON missing:\n%s", out)
	}
}

func TestIntgToolsRefresh_BadJSONResult(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.TextResponse(200, "{nope"))
	covRefreshTools(t, "search")

	err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "decode refresh response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestIntgToolsListAndRefresh_AuthGates(t *testing.T) {
	covSaveState(t)
	args := []string{covCrew, "intg1"}
	for _, cmd := range []struct {
		name string
		run  func() error
	}{
		{"list", func() error { return intgToolsListCmd.RunE(intgToolsListCmd, args) }},
		{"refresh", func() error { return intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, args) }},
	} {
		cliCfg = &cli.CLIConfig{}
		if err := cmd.run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected not-logged-in; got %v", cmd.name, err)
		}
		cliCfg = &cli.CLIConfig{Token: "tok"}
		if err := cmd.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error; got %v", cmd.name, err)
		}
	}
}

func TestIntgTools_TransportErrors(t *testing.T) {
	covStubDown(t)
	covRefreshTools(t, "search")
	if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"}); err == nil {
		t.Error("list: expected transport error")
	}
	if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"}); err == nil {
		t.Error("refresh: expected transport error")
	}
	if err := toggleCrewIntegrationTool(covCrew, "intg1", "t1", true); err == nil {
		t.Error("toggle: expected transport error")
	}
}

func TestIntgTools_CrewResolutionFailures(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covRefreshTools(t, "search")
	if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{"ghost", "intg1"}); err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("list: expected crew-not-found; got %v", err)
	}
	if err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{"ghost", "intg1"}); err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("refresh: expected crew-not-found; got %v", err)
	}
}

func TestIntgToolsList_DecodeError(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews/"+covCrew+"/integrations/intg1/tools", clitest.TextResponse(200, "{nope"))
	if err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"}); err == nil {
		t.Error("expected decode error on malformed tools body")
	}
}

func TestIntgToolsRefresh_ServerError(t *testing.T) {
	s := covStubCli9(t)
	s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.ErrorResponse(500, "refresh broke"))
	covRefreshTools(t, "search")
	err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "refresh broke") {
		t.Errorf("expected 500 error; got %v", err)
	}
}

// A 4xx from the endpoint must surface through cli.CheckError with the
// server's detail and the matching exit class — not be swallowed into the
// "refresh requested" confirmation.
func TestIntgToolsRefresh_ClientErrorSurfaces(t *testing.T) {
	for _, tc := range []struct {
		status int
		detail string
		want   int
	}{
		{400, "Invalid JSON body", cli.ExitValidation},
		{403, "Forbidden", cli.ExitAuth},
		{404, "Crew integration not found", cli.ExitNotFound},
	} {
		t.Run(tc.detail, func(t *testing.T) {
			s := covStubCli9(t)
			s.OnPost("/api/v1/crews/"+covCrew+"/integrations/intg1/tools/refresh", clitest.ErrorResponse(tc.status, tc.detail))
			covRefreshTools(t, "search=Full-text search")

			err := intgToolsRefreshCmd.RunE(intgToolsRefreshCmd, []string{covCrew, "intg1"})
			if err == nil || !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("expected the %d detail to surface; got %v", tc.status, err)
			}
			if code := cli.ExitCodeFor(err); code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
			// The tools still went out: a rejected refresh must not be a
			// silently-empty one.
			if body := covRefreshBody(t, s); len(body["tools"].([]any)) != 1 {
				t.Errorf("tools = %#v, want the discovered entry", body["tools"])
			}
		})
	}
}

func TestIntgToolsList_ServerError(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/crews/"+covCrew+"/integrations/intg1/tools", clitest.ErrorResponse(403, "no access"))
	err := intgToolsListCmd.RunE(intgToolsListCmd, []string{covCrew, "intg1"})
	if err == nil || !strings.Contains(err.Error(), "no access") {
		t.Errorf("expected 403 error; got %v", err)
	}
}
