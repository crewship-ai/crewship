package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSlugifyName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"My Routine", "my-routine"},
		{"  Email Fetch / Summarize: v2  ", "email-fetch-summarize-v2"},
		{"already-slugged", "already-slugged"},
		{"under_score kept", "under_score-kept"},
		{"Trailing dots...", "trailing-dots"},
		{"UPPER case", "upper-case"},
		{"", ""},
		{"!!!", ""},
		{strings.Repeat("a", 80), strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		if got := slugifyName(tc.in); got != tc.want {
			t.Errorf("slugifyName(%q): got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestIndent(t *testing.T) {
	t.Parallel()
	if got := indent("", "  "); got != "" {
		t.Errorf("indent empty: got %q", got)
	}
	if got := indent("a\nb", "> "); got != "> a\n> b" {
		t.Errorf("indent multiline: got %q", got)
	}
}

func pipelinesPathCov() string {
	return fmt.Sprintf("/api/v1/workspaces/%s/pipelines", covWorkspaceID)
}

func TestPipelineListRunE(t *testing.T) {
	t.Run("empty list prints hint", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(pipelinesPathCov(), clitest.JSONResponse(200, []any{}))

		out, err := captureStdoutCov(t, func() error {
			return pipelineListCmd.RunE(pipelineListCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "No routines registered yet.") {
			t.Errorf("stdout: %q", out)
		}
	})

	t.Run("rows rendered with truncation and order param", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)

		longDesc := strings.Repeat("d", 80)
		stub.OnGet(pipelinesPathCov(), clitest.JSONResponse(200, []map[string]any{
			{"slug": "email-fetch", "invocation_count": 7, "last_invocation_status": "COMPLETED",
				"author_crew_id": "crew_a", "description": longDesc},
			{"slug": "no-status", "invocation_count": 0, "description": "short"},
		}))
		setFlagCov(t, pipelineListCmd, "order", "recent")

		out, err := captureStdoutCov(t, func() error {
			return pipelineListCmd.RunE(pipelineListCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		for _, want := range []string{"email-fetch", "COMPLETED", "crew_a", strings.Repeat("d", 57) + "...", "—"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		calls := stub.CallsFor("GET", pipelinesPathCov())
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "order=recent") {
			t.Errorf("expected order=recent in query; calls=%+v", calls)
		}
	})

	t.Run("api error surfaced", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(pipelinesPathCov(), clitest.ErrorResponse(500, "boom"))

		if err := pipelineListCmd.RunE(pipelineListCmd, nil); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("want server error, got %v", err)
		}
	})

	t.Run("auth required", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		if err := pipelineListCmd.RunE(pipelineListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Fatalf("want not logged in, got %v", err)
		}
	})
}

func TestPipelineGetRunE(t *testing.T) {
	row := map[string]any{
		"id": "p1", "slug": "email-fetch", "name": "Email Fetch",
		"description": "fetches", "dsl_version": "1",
		"author_crew_id": "crew_a", "author_agent_id": "ag_1", "authored_via": "cli",
		"invocation_count": 3, "last_invoked_at": "2026-01-01T00:00:00Z",
		"last_invocation_status": "COMPLETED",
		"created_at":             "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z",
		"definition": map[string]any{"steps": []any{}},
	}

	t.Run("human format", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(pipelinesPathCov()+"/email-fetch", clitest.JSONResponse(200, row))

		out, err := captureStdoutCov(t, func() error {
			return pipelineGetCmd.RunE(pipelineGetCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		for _, want := range []string{"Slug:", "email-fetch", "Last invoked:", "status=COMPLETED", "Definition:", `"steps"`} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("json format", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(pipelinesPathCov()+"/email-fetch", clitest.JSONResponse(200, row))
		setFlagCov(t, pipelineGetCmd, "format", "json")

		out, err := captureStdoutCov(t, func() error {
			return pipelineGetCmd.RunE(pipelineGetCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		var decoded pipelineRowJSON
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("output not JSON: %v\n%s", err, out)
		}
		if decoded.Slug != "email-fetch" || decoded.InvocationCount != 3 {
			t.Errorf("decoded row: %+v", decoded)
		}
	})

	t.Run("unknown format rejected", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(pipelinesPathCov()+"/email-fetch", clitest.JSONResponse(200, row))
		setFlagCov(t, pipelineGetCmd, "format", "xml")

		err := pipelineGetCmd.RunE(pipelineGetCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), `unknown --format "xml"`) {
			t.Fatalf("want unknown format error, got %v", err)
		}
	})
}

func TestPipelineSaveRunE(t *testing.T) {
	writeDefinition := func(t *testing.T) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "routine.json")
		if err := os.WriteFile(p, []byte(`{"name":"x","steps":[]}`), 0o600); err != nil {
			t.Fatalf("write definition: %v", err)
		}
		return p
	}

	t.Run("required flags", func(t *testing.T) {
		cases := []struct {
			name    string
			set     map[string]string
			wantErr string
		}{
			{"definition required", map[string]string{}, "--definition <path> required"},
			{"name required", map[string]string{"definition": "x.json"}, "--name required"},
			{"author-crew required", map[string]string{"definition": "x.json", "name": "X"}, "--author-crew required"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				saveCLIState(t)
				cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
				for k, v := range tc.set {
					setFlagCov(t, pipelineSaveCmd, k, v)
				}
				err := pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want %q, got %v", tc.wantErr, err)
				}
			})
		}
	})

	t.Run("unreadable definition file", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, pipelineSaveCmd, "definition", filepath.Join(t.TempDir(), "missing.json"))
		setFlagCov(t, pipelineSaveCmd, "name", "X")
		setFlagCov(t, pipelineSaveCmd, "author-crew", "crew_a")

		err := pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "read definition file") {
			t.Fatalf("want read error, got %v", err)
		}
	})

	t.Run("bad sample inputs", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, pipelineSaveCmd, "definition", writeDefinition(t))
		setFlagCov(t, pipelineSaveCmd, "name", "X")
		setFlagCov(t, pipelineSaveCmd, "author-crew", "crew_a")
		setFlagCov(t, pipelineSaveCmd, "sample-inputs", "{not json")

		err := pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "parse --sample-inputs JSON") {
			t.Fatalf("want sample-inputs parse error, got %v", err)
		}
	})

	t.Run("test_run failure blocks save", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(pipelinesPathCov()+"/test_run", clitest.JSONResponse(200, map[string]any{
			"status": "FAILED", "error_message": "step 2 exploded",
		}))
		setFlagCov(t, pipelineSaveCmd, "definition", writeDefinition(t))
		setFlagCov(t, pipelineSaveCmd, "name", "X")
		setFlagCov(t, pipelineSaveCmd, "author-crew", "crew_a")

		_, err := captureStdoutCov(t, func() error {
			return pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		})
		if err == nil || !strings.Contains(err.Error(), "test_run did not complete cleanly") ||
			!strings.Contains(err.Error(), "step 2 exploded") {
			t.Fatalf("want test_run failure error, got %v", err)
		}
		if n := len(stub.CallsFor("POST", pipelinesPathCov()+"/save")); n != 0 {
			t.Errorf("save endpoint must not be called after failed test_run; got %d calls", n)
		}
	})

	t.Run("happy path runs gate then saves", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(pipelinesPathCov()+"/test_run", clitest.JSONResponse(200, map[string]any{
			"status": "COMPLETED", "duration_ms": 120, "cost_usd": 0.01,
			"save_token": "tok-happy",
		}))
		stub.OnPost(pipelinesPathCov()+"/save", clitest.JSONResponse(200, map[string]any{
			"id": "p9", "slug": "email-fetch-v2",
			"definition_hash": "abcdef0123456789deadbeef",
		}))
		setFlagCov(t, pipelineSaveCmd, "definition", writeDefinition(t))
		setFlagCov(t, pipelineSaveCmd, "name", "Email Fetch v2")
		setFlagCov(t, pipelineSaveCmd, "description", "fetches mail")
		setFlagCov(t, pipelineSaveCmd, "author-crew", "crew_a")
		setFlagCov(t, pipelineSaveCmd, "author-agent", "ag_1")
		setFlagCov(t, pipelineSaveCmd, "sample-inputs", `{"since":"yesterday"}`)

		out, err := captureStdoutCov(t, func() error {
			return pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		for _, want := range []string{"test_run passed", "Saved routine email-fetch-v2", "hash=abcdef012345", "crewship routine run email-fetch-v2"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}

		testCalls := stub.CallsFor("POST", pipelinesPathCov()+"/test_run")
		if len(testCalls) != 1 {
			t.Fatalf("expected one test_run POST, got %d", len(testCalls))
		}
		var testBody map[string]any
		clitest.MustDecodeJSONBody(testCalls[0].Body, &testBody)
		if testBody["author_crew_id"] != "crew_a" {
			t.Errorf("test_run body author_crew_id: %v", testBody["author_crew_id"])
		}
		if si, ok := testBody["sample_inputs"].(map[string]any); !ok || si["since"] != "yesterday" {
			t.Errorf("test_run sample_inputs: %v", testBody["sample_inputs"])
		}

		saveCalls := stub.CallsFor("POST", pipelinesPathCov()+"/save")
		if len(saveCalls) != 1 {
			t.Fatalf("expected one save POST, got %d", len(saveCalls))
		}
		var saveBody map[string]any
		clitest.MustDecodeJSONBody(saveCalls[0].Body, &saveBody)
		if saveBody["slug"] != "email-fetch-v2" {
			t.Errorf("save slug: got %v want email-fetch-v2 (slugified from name)", saveBody["slug"])
		}
		// Post-#655: the user-facing save carries the HMAC save_token minted by
		// test_run (not a self-claimed boolean), and the workspace comes from the
		// URL path. author_agent is recorded only on the sidecar path.
		if saveBody["author_crew_id"] != "crew_a" {
			t.Errorf("save body author_crew_id: got %v want crew_a", saveBody["author_crew_id"])
		}
		if saveBody["save_token"] != "tok-happy" {
			t.Errorf("save body must carry the HMAC save_token from test_run; got %v", saveBody["save_token"])
		}
	})
}

func TestPipelineRunRunE(t *testing.T) {
	runPath := pipelinesPathCov() + "/email-fetch/run"

	t.Run("bad inputs JSON", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, pipelineRunCmd, "inputs", "{nope")

		err := pipelineRunCmd.RunE(pipelineRunCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "parse --inputs JSON") {
			t.Fatalf("want inputs parse error, got %v", err)
		}
	})

	t.Run("completed run prints output and step outputs", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(runPath, clitest.JSONResponse(200, map[string]any{
			"run_id": "run_1", "status": "COMPLETED", "output": "final answer",
			"step_outputs": map[string]string{"step1": strings.Repeat("x", 250)},
			"duration_ms":  90, "cost_usd": 0.02,
		}))
		setFlagCov(t, pipelineRunCmd, "inputs", `{"since":"yesterday"}`)
		setFlagCov(t, pipelineRunCmd, "tier-override", "fast")

		out, err := captureStdoutCov(t, func() error {
			return pipelineRunCmd.RunE(pipelineRunCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		for _, want := range []string{"Run run_1: COMPLETED", "final answer", "Step outputs:", "[step1]", "..."} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}

		calls := stub.CallsFor("POST", runPath)
		if len(calls) != 1 {
			t.Fatalf("expected one run POST, got %d", len(calls))
		}
		var body map[string]any
		clitest.MustDecodeJSONBody(calls[0].Body, &body)
		if body["tier_override"] != "fast" {
			t.Errorf("tier_override: %v", body["tier_override"])
		}
		if in, ok := body["inputs"].(map[string]any); !ok || in["since"] != "yesterday" {
			t.Errorf("inputs: %v", body["inputs"])
		}
	})

	t.Run("failed run returns error with step detail", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(runPath, clitest.JSONResponse(200, map[string]any{
			"run_id": "run_2", "status": "FAILED",
			"failed_at_step": "step3", "error_message": "agent timeout",
		}))

		out, err := captureStdoutCov(t, func() error {
			return pipelineRunCmd.RunE(pipelineRunCmd, []string{"email-fetch"})
		})
		if err == nil || !strings.Contains(err.Error(), "routine run failed") {
			t.Fatalf("want 'routine run failed', got %v", err)
		}
		if !strings.Contains(out, "failed at step: step3") || !strings.Contains(out, "agent timeout") {
			t.Errorf("stdout missing failure detail:\n%s", out)
		}
	})

	t.Run("404 suggests similar slugs", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(pipelinesPathCov()+"/email-fetc/run", clitest.ErrorResponse(404, "pipeline not found"))
		stub.OnGet(pipelinesPathCov(), clitest.JSONResponse(200, []map[string]string{
			{"slug": "email-fetch"}, {"slug": "totally-different"},
		}))

		err := pipelineRunCmd.RunE(pipelineRunCmd, []string{"email-fetc"})
		if err == nil || !strings.Contains(err.Error(), `routine "email-fetc" not found`) ||
			!strings.Contains(err.Error(), "did you mean: email-fetch") {
			t.Fatalf("want did-you-mean hint, got %v", err)
		}
	})
}

func TestPipelineDryRunRunE(t *testing.T) {
	dryPath := pipelinesPathCov() + "/email-fetch/dry_run"

	t.Run("bad inputs JSON", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
		setFlagCov(t, pipelineDryRunCmd, "inputs", "{nope")

		err := pipelineDryRunCmd.RunE(pipelineDryRunCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "parse --inputs JSON") {
			t.Fatalf("want inputs parse error, got %v", err)
		}
	})

	t.Run("renders would_execute plan", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(dryPath, clitest.JSONResponse(200, map[string]any{
			"status": "DRY_RUN_OK", "duration_ms": 5, "cost_usd": 0.001,
			"would_execute": []map[string]any{
				{"step_id": "fetch", "step_type": "agent_run", "would_call_agent": "viktor",
					"tier_adapter": "CLAUDE_CODE", "tier_model": "haiku",
					"estimated_cost_usd": 0.0005, "would_pass": strings.Repeat("p", 350)},
				{"step_id": "chain", "step_type": "pipeline_call", "would_call_pipeline": "summarize"},
			},
		}))
		setFlagCov(t, pipelineDryRunCmd, "inputs", `{"since":"1d"}`)

		out, err := captureStdoutCov(t, func() error {
			return pipelineDryRunCmd.RunE(pipelineDryRunCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		for _, want := range []string{
			"Dry run: DRY_RUN_OK", "Step 1 [fetch] (agent_run)", "would call agent: viktor",
			"resolved tier: CLAUDE_CODE/haiku", "estimated cost:", "rendered prompt:",
			"Step 2 [chain] (pipeline_call)", "would call routine: summarize", "...",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}

		calls := stub.CallsFor("POST", dryPath)
		if len(calls) != 1 {
			t.Fatalf("expected one dry_run POST, got %d", len(calls))
		}
		var body map[string]any
		clitest.MustDecodeJSONBody(calls[0].Body, &body)
		if in, ok := body["inputs"].(map[string]any); !ok || in["since"] != "1d" {
			t.Errorf("dry_run inputs: %v", body["inputs"])
		}
	})

	t.Run("empty inputs default to {}", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(dryPath, clitest.JSONResponse(200, map[string]any{"status": "DRY_RUN_OK"}))

		_, err := captureStdoutCov(t, func() error {
			return pipelineDryRunCmd.RunE(pipelineDryRunCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		calls := stub.CallsFor("POST", dryPath)
		if len(calls) != 1 {
			t.Fatalf("expected one POST, got %d", len(calls))
		}
		if got := strings.TrimSpace(string(calls[0].Body)); got != `{"inputs":{}}` {
			t.Errorf("default body: got %q", got)
		}
	})
}

func TestPipelineDeleteRunE(t *testing.T) {
	t.Run("deletes after --yes", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnDelete(pipelinesPathCov()+"/email-fetch", clitest.EmptyResponse(204))
		setFlagCov(t, pipelineDeleteCmd, "yes", "true")

		out, err := captureStdoutCov(t, func() error {
			return pipelineDeleteCmd.RunE(pipelineDeleteCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "Deleted routine email-fetch") {
			t.Errorf("stdout: %q", out)
		}
		if n := len(stub.CallsFor("DELETE", pipelinesPathCov()+"/email-fetch")); n != 1 {
			t.Errorf("expected one DELETE, got %d", n)
		}
	})

	t.Run("api error surfaced", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnDelete(pipelinesPathCov()+"/ghost", clitest.ErrorResponse(404, "pipeline not found"))
		setFlagCov(t, pipelineDeleteCmd, "yes", "true")

		err := pipelineDeleteCmd.RunE(pipelineDeleteCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "pipeline not found") {
			t.Fatalf("want not-found error, got %v", err)
		}
	})
}

func TestPipelineRunsRunE(t *testing.T) {
	runsPath := pipelinesPathCov() + "/email-fetch/runs"

	t.Run("no runs yet", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(runsPath, clitest.JSONResponse(200, []any{}))

		out, err := captureStdoutCov(t, func() error {
			return pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "No runs yet for this routine.") {
			t.Errorf("stdout: %q", out)
		}
	})

	t.Run("rows rendered, run id truncated, limit clamped", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(runsPath, clitest.JSONResponse(200, []map[string]any{
			{"id": "j1", "ts": "2026-01-01T00:00:00Z", "entry_type": "pipeline.completed",
				"severity": "info", "summary": "ok", "run_id": "run_aaaaaaaaaaaaaaaaaaaaaa"},
		}))
		setFlagCov(t, pipelineRunsCmd, "limit", "0") // <=0 must clamp to 20

		out, err := captureStdoutCov(t, func() error {
			return pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"email-fetch"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if !strings.Contains(out, "pipeline.completed") || !strings.Contains(out, "run_aaaaaaaaaaaa…") {
			t.Errorf("stdout:\n%s", out)
		}
		calls := stub.CallsFor("GET", runsPath)
		if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=20") {
			t.Errorf("expected limit=20 in query, calls=%+v", calls)
		}
	})
}

// TestPipelineSubcommands_AuthGates pins requireAuth/requireWorkspace on
// every routine subcommand.
func TestPipelineSubcommands_AuthGates(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{"get", func() error { return pipelineGetCmd.RunE(pipelineGetCmd, []string{"s"}) }},
		{"save", func() error { return pipelineSaveCmd.RunE(pipelineSaveCmd, nil) }},
		{"run", func() error { return pipelineRunCmd.RunE(pipelineRunCmd, []string{"s"}) }},
		{"dry-run", func() error { return pipelineDryRunCmd.RunE(pipelineDryRunCmd, []string{"s"}) }},
		{"delete", func() error { return pipelineDeleteCmd.RunE(pipelineDeleteCmd, []string{"s"}) }},
		{"runs", func() error { return pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"s"}) }},
		{"list-ws", func() error { return pipelineListCmd.RunE(pipelineListCmd, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Fatalf("want not logged in, got %v", err)
			}
		})
		t.Run(tc.name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{Token: "fake-token"}
			flagWorkspace = ""
			t.Setenv("CREWSHIP_WORKSPACE", "")
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Fatalf("want workspace error, got %v", err)
			}
		})
	}
}

// TestPipelineCommands_NetworkError covers the transport-failure branch
// after each subcommand's first HTTP call.
func TestPipelineCommands_NetworkError(t *testing.T) {
	defPath := filepath.Join(t.TempDir(), "def.json")
	if err := os.WriteFile(defPath, []byte(`{"steps":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{"list", func(t *testing.T) error { return pipelineListCmd.RunE(pipelineListCmd, nil) }},
		{"get", func(t *testing.T) error { return pipelineGetCmd.RunE(pipelineGetCmd, []string{"s"}) }},
		{"run", func(t *testing.T) error { return pipelineRunCmd.RunE(pipelineRunCmd, []string{"s"}) }},
		{"dry-run", func(t *testing.T) error { return pipelineDryRunCmd.RunE(pipelineDryRunCmd, []string{"s"}) }},
		{"runs", func(t *testing.T) error { return pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"s"}) }},
		{"delete", func(t *testing.T) error {
			setFlagCov(t, pipelineDeleteCmd, "yes", "true")
			return pipelineDeleteCmd.RunE(pipelineDeleteCmd, []string{"s"})
		}},
		{"save", func(t *testing.T) error {
			setFlagCov(t, pipelineSaveCmd, "definition", defPath)
			setFlagCov(t, pipelineSaveCmd, "name", "X")
			setFlagCov(t, pipelineSaveCmd, "author-crew", "crew_a")
			_, err := captureStdoutCov(t, func() error {
				return pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
			})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupDeadCLICov(t)
			if err := tc.run(t); err == nil {
				t.Fatal("want connection error against dead server")
			}
		})
	}
}

func TestPipelineGetRunE_ErrorPaths(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(pipelinesPathCov()+"/ghost", clitest.ErrorResponse(404, "pipeline not found"))
		err := pipelineGetCmd.RunE(pipelineGetCmd, []string{"ghost"})
		if err == nil || !strings.Contains(err.Error(), "pipeline not found") {
			t.Fatalf("want 404 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(pipelinesPathCov()+"/bad", clitest.TextResponse(200, "not json"))
		err := pipelineGetCmd.RunE(pipelineGetCmd, []string{"bad"})
		if err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
}

func TestPipelineListRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	stub.OnGet(pipelinesPathCov(), clitest.TextResponse(200, "not json"))
	err := pipelineListCmd.RunE(pipelineListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("want decode error, got %v", err)
	}
}

func TestPipelineSaveRunE_ServerSideErrors(t *testing.T) {
	writeDef := func(t *testing.T) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "routine.json")
		if err := os.WriteFile(p, []byte(`{"steps":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	setSaveFlags := func(t *testing.T) {
		t.Helper()
		setFlagCov(t, pipelineSaveCmd, "definition", writeDef(t))
		setFlagCov(t, pipelineSaveCmd, "name", "X")
		setFlagCov(t, pipelineSaveCmd, "author-crew", "crew_a")
	}

	t.Run("test_run rejected by server", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(pipelinesPathCov()+"/test_run", clitest.ErrorResponse(422, "DSL invalid"))
		setSaveFlags(t)

		_, err := captureStdoutCov(t, func() error {
			return pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		})
		if err == nil || !strings.Contains(err.Error(), "test_run failed") {
			t.Fatalf("want test_run failed wrap, got %v", err)
		}
	})

	t.Run("test_run decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(pipelinesPathCov()+"/test_run", clitest.TextResponse(200, "not json"))
		setSaveFlags(t)

		_, err := captureStdoutCov(t, func() error {
			return pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		})
		if err == nil || !strings.Contains(err.Error(), "decode test_run response") {
			t.Fatalf("want decode error, got %v", err)
		}
	})

	t.Run("save rejected by server", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(pipelinesPathCov()+"/test_run", clitest.JSONResponse(200, map[string]any{"status": "COMPLETED"}))
		stub.OnPost(pipelinesPathCov()+"/save", clitest.ErrorResponse(403, "not yours"))
		setSaveFlags(t)

		_, err := captureStdoutCov(t, func() error {
			return pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		})
		if err == nil || !strings.Contains(err.Error(), "not yours") {
			t.Fatalf("want save 403 surfaced, got %v", err)
		}
	})

	t.Run("save decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(pipelinesPathCov()+"/test_run", clitest.JSONResponse(200, map[string]any{"status": "COMPLETED"}))
		stub.OnPost(pipelinesPathCov()+"/save", clitest.TextResponse(200, "not json"))
		setSaveFlags(t)

		_, err := captureStdoutCov(t, func() error {
			return pipelineSaveCmd.RunE(pipelineSaveCmd, nil)
		})
		if err == nil || !strings.Contains(err.Error(), "decode save response") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
}

func TestPipelineRunRunE_ServerSideErrors(t *testing.T) {
	runPath := pipelinesPathCov() + "/email-fetch/run"

	t.Run("non-404 error surfaced directly", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(runPath, clitest.ErrorResponse(500, "tier exploded"))
		err := pipelineRunCmd.RunE(pipelineRunCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "tier exploded") {
			t.Fatalf("want 500 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(runPath, clitest.TextResponse(200, "not json"))
		err := pipelineRunCmd.RunE(pipelineRunCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "decode run response") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
}

func TestPipelineDryRunRunE_ServerSideErrors(t *testing.T) {
	dryPath := pipelinesPathCov() + "/email-fetch/dry_run"

	t.Run("server error surfaced", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(dryPath, clitest.ErrorResponse(500, "resolver broke"))
		err := pipelineDryRunCmd.RunE(pipelineDryRunCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "resolver broke") {
			t.Fatalf("want 500 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnPost(dryPath, clitest.TextResponse(200, "not json"))
		err := pipelineDryRunCmd.RunE(pipelineDryRunCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "decode dry_run response") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
}

// TestPipelineDeleteRunE_AbortedConfirm covers the confirmAction error
// branch: without --yes and with stdin/stdout being pipes (non-TTY under
// go test), the plain-stdin fallback reads EOF and aborts.
func TestPipelineDeleteRunE_AbortedConfirm(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	setFlagCov(t, pipelineDeleteCmd, "yes", "false")

	_, err := captureStdoutCov(t, func() error {
		return pipelineDeleteCmd.RunE(pipelineDeleteCmd, []string{"email-fetch"})
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want aborted, got %v", err)
	}
	if n := len(stub.CallsFor("DELETE", pipelinesPathCov()+"/email-fetch")); n != 0 {
		t.Errorf("DELETE must not fire after aborted confirm; got %d", n)
	}
}

func TestPipelineRunsRunE_ServerSideErrors(t *testing.T) {
	runsPath := pipelinesPathCov() + "/email-fetch/runs"

	t.Run("server error surfaced", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(runsPath, clitest.ErrorResponse(500, "journal down"))
		err := pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "journal down") {
			t.Fatalf("want 500 surfaced, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet(runsPath, clitest.TextResponse(200, "not json"))
		err := pipelineRunsCmd.RunE(pipelineRunsCmd, []string{"email-fetch"})
		if err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
}
