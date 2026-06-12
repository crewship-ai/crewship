package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// ─── flag-copy helpers ──────────────────────────────────────────────────

func TestSetStringFlag(t *testing.T) {
	c := &cobra.Command{Use: "x"}
	c.Flags().String("crew", "", "")
	body := map[string]any{}

	setStringFlag(c, body, "crew", "crew_id")
	if _, ok := body["crew_id"]; ok {
		t.Error("empty flag must be omitted")
	}

	if err := c.Flags().Set("crew", "crew-1"); err != nil {
		t.Fatal(err)
	}
	setStringFlag(c, body, "crew", "crew_id")
	if body["crew_id"] != "crew-1" {
		t.Errorf("crew_id: got %v", body["crew_id"])
	}
}

func TestSetChangedString(t *testing.T) {
	c := &cobra.Command{Use: "x"}
	c.Flags().String("crew", "", "")
	body := map[string]any{}

	setChangedString(c.Flags(), body, "crew", "crew_id")
	if _, ok := body["crew_id"]; ok {
		t.Error("unchanged flag must be omitted")
	}

	// An explicit empty string IS sent — the clear-to-NULL contract.
	if err := c.Flags().Set("crew", ""); err != nil {
		t.Fatal(err)
	}
	setChangedString(c.Flags(), body, "crew", "crew_id")
	if v, ok := body["crew_id"]; !ok || v != "" {
		t.Errorf("explicit empty string should be sent: %v ok=%v", v, ok)
	}
}

func TestSetJSONFlag(t *testing.T) {
	newC := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		c.Flags().String("labels", "", "")
		return c
	}

	t.Run("empty omitted", func(t *testing.T) {
		body := map[string]any{}
		if err := setJSONFlag(newC(), body, "labels", "labels_json"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("body should stay empty: %v", body)
		}
	})
	t.Run("invalid json rejected", func(t *testing.T) {
		c := newC()
		if err := c.Flags().Set("labels", "{not json"); err != nil {
			t.Fatal(err)
		}
		err := setJSONFlag(c, map[string]any{}, "labels", "labels_json")
		if err == nil || !strings.Contains(err.Error(), "--labels must be valid JSON") {
			t.Fatalf("expected JSON error, got %v", err)
		}
	})
	t.Run("valid json copied", func(t *testing.T) {
		c := newC()
		if err := c.Flags().Set("labels", `["a","b"]`); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{}
		if err := setJSONFlag(c, body, "labels", "labels_json"); err != nil {
			t.Fatalf("setJSONFlag: %v", err)
		}
		if body["labels_json"] != `["a","b"]` {
			t.Errorf("labels_json: got %v", body["labels_json"])
		}
	})
}

func TestSetChangedJSON(t *testing.T) {
	newC := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		c.Flags().String("labels", "", "")
		return c
	}

	t.Run("unchanged is no-op", func(t *testing.T) {
		body := map[string]any{}
		if err := setChangedJSON(newC().Flags(), body, "labels", "labels_json"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(body) != 0 {
			t.Errorf("body should stay empty: %v", body)
		}
	})
	t.Run("explicit empty passes through", func(t *testing.T) {
		c := newC()
		if err := c.Flags().Set("labels", ""); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{}
		if err := setChangedJSON(c.Flags(), body, "labels", "labels_json"); err != nil {
			t.Fatalf("setChangedJSON: %v", err)
		}
		if v, ok := body["labels_json"]; !ok || v != "" {
			t.Errorf("explicit empty should be sent: %v ok=%v", v, ok)
		}
	})
	t.Run("invalid json rejected", func(t *testing.T) {
		c := newC()
		if err := c.Flags().Set("labels", "{oops"); err != nil {
			t.Fatal(err)
		}
		err := setChangedJSON(c.Flags(), map[string]any{}, "labels", "labels_json")
		if err == nil || !strings.Contains(err.Error(), "must be valid JSON") {
			t.Fatalf("expected JSON error, got %v", err)
		}
	})
}

// ─── emitFormattedJSON ──────────────────────────────────────────────────

func TestEmitFormattedJSON(t *testing.T) {
	newC := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		jqExprFlag(c)
		return c
	}

	t.Run("default pretty json", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = nil
		out := covCaptureStdoutCli3(t, func() {
			if err := emitFormattedJSON(newC(), map[string]any{"k": "v"}); err != nil {
				t.Errorf("emitFormattedJSON: %v", err)
			}
		})
		if !strings.Contains(out, `"k": "v"`) {
			t.Errorf("pretty json missing: %q", out)
		}
	})

	t.Run("marshal error", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = nil
		if err := emitFormattedJSON(newC(), make(chan int)); err == nil {
			t.Fatal("expected marshal error")
		}
	})

	t.Run("yaml", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Format: "yaml"}
		out := covCaptureStdoutCli3(t, func() {
			if err := emitFormattedJSON(newC(), map[string]any{"k": "v"}); err != nil {
				t.Errorf("emitFormattedJSON yaml: %v", err)
			}
		})
		if !strings.Contains(out, "k: v") {
			t.Errorf("yaml output missing: %q", out)
		}
	})

	t.Run("quiet emits nothing", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{Format: "quiet"}
		out := covCaptureStdoutCli3(t, func() {
			if err := emitFormattedJSON(newC(), map[string]any{"k": "v"}); err != nil {
				t.Errorf("emitFormattedJSON quiet: %v", err)
			}
		})
		if out != "" {
			t.Errorf("quiet should print nothing, got %q", out)
		}
	})

	t.Run("filter path", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = nil
		covSwapJQ(t,
			func(string) (string, error) { return "/fake/jq", nil },
			func(string, string) jqRunner { return &fakeJQCov{out: []byte("\"v\"\n")} })
		c := newC()
		if err := c.Flags().Set("filter", ".k"); err != nil {
			t.Fatal(err)
		}
		out := covCaptureStdoutCli3(t, func() {
			if err := emitFormattedJSON(c, map[string]any{"k": "v"}); err != nil {
				t.Errorf("emitFormattedJSON filter: %v", err)
			}
		})
		if out != "\"v\"\n" {
			t.Errorf("filtered output: %q", out)
		}
	})
}

// ─── triage ─────────────────────────────────────────────────────────────

func TestTriageListCmd_RunE(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/triage-rules",
		clitest.JSONResponse(200, []map[string]any{{"id": "tr1", "name": "route-bugs"}}))

	out := covCaptureStdoutCli3(t, func() {
		if err := triageListCmd.RunE(triageListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "route-bugs") {
		t.Errorf("rule missing from output: %q", out)
	}
}

func TestTriageProcessCmd_RunE(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/triage/process",
		clitest.JSONResponse(200, map[string]any{"processed": 3}))

	out := covCaptureStdoutCli3(t, func() {
		if err := triageProcessCmd.RunE(triageProcessCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"processed": 3`) {
		t.Errorf("process result missing: %q", out)
	}
	if len(stub.CallsFor("POST", "/api/v1/triage/process")) != 1 {
		t.Error("expected 1 process POST")
	}
}

func TestTriageCreateCmd_HappyPath(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, triageCreateCmd)
	stub.OnPost("/api/v1/triage-rules",
		clitest.JSONResponse(201, map[string]any{"id": "tr1"}))

	covSetFlags(t, triageCreateCmd, map[string]string{
		"name": "route-bugs", "pattern": "bug:", "match-type": "contains",
		"crew": "crew-1", "labels": `["bug"]`,
	})
	out := covCaptureStdoutCli3(t, func() {
		if err := triageCreateCmd.RunE(triageCreateCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "tr1") {
		t.Errorf("created rule missing: %q", out)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/triage-rules")[0].Body, &body)
	if body["name"] != "route-bugs" || body["pattern"] != "bug:" || body["match_type"] != "contains" {
		t.Errorf("create body wrong: %v", body)
	}
	if body["crew_id"] != "crew-1" || body["labels_json"] != `["bug"]` {
		t.Errorf("optional fields wrong: %v", body)
	}
}

func TestTriageCreateCmd_InvalidLabelsJSON(t *testing.T) {
	covStub(t)
	covResetFlags(t, triageCreateCmd)
	covSetFlags(t, triageCreateCmd, map[string]string{
		"name": "n", "pattern": "p", "match-type": "exact", "labels": "{bad",
	})
	err := triageCreateCmd.RunE(triageCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--labels must be valid JSON") {
		t.Fatalf("expected labels error, got %v", err)
	}
}

func TestTriageUpdateCmd_HappyPath(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, triageUpdateCmd)
	stub.OnPatch("/api/v1/triage-rules/tr1",
		clitest.JSONResponse(200, map[string]any{"id": "tr1"}))

	covSetFlags(t, triageUpdateCmd, map[string]string{
		"name": "renamed", "match-type": "regex", "position": "2", "enabled": "false",
	})
	if err := triageUpdateCmd.RunE(triageUpdateCmd, []string{"tr1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/triage-rules/tr1")[0].Body, &body)
	if body["name"] != "renamed" || body["match_type"] != "regex" {
		t.Errorf("update body wrong: %v", body)
	}
	if pos, ok := body["position"].(float64); !ok || pos != 2 {
		t.Errorf("position wrong: %v", body["position"])
	}
	if enabled, ok := body["enabled"].(bool); !ok || enabled {
		t.Errorf("enabled=false should be sent: %v", body["enabled"])
	}
}

func TestTriageUpdateCmd_BadMatchType(t *testing.T) {
	covStub(t)
	covResetFlags(t, triageUpdateCmd)
	covSetFlags(t, triageUpdateCmd, map[string]string{"match-type": "fuzzy"})
	err := triageUpdateCmd.RunE(triageUpdateCmd, []string{"tr1"})
	if err == nil || !strings.Contains(err.Error(), "--match-type must be one of") {
		t.Fatalf("expected match-type error, got %v", err)
	}
}

func TestTriageUpdateCmd_BadLabels(t *testing.T) {
	covStub(t)
	covResetFlags(t, triageUpdateCmd)
	covSetFlags(t, triageUpdateCmd, map[string]string{"labels": "{nope"})
	err := triageUpdateCmd.RunE(triageUpdateCmd, []string{"tr1"})
	if err == nil || !strings.Contains(err.Error(), "must be valid JSON") {
		t.Fatalf("expected labels error, got %v", err)
	}
}

func TestTriageDeleteCmd(t *testing.T) {
	t.Run("happy with --yes", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, triageDeleteCmd)
		stub.OnDelete("/api/v1/triage-rules/tr1", clitest.EmptyResponse(204))
		covSetFlags(t, triageDeleteCmd, map[string]string{"yes": "true"})
		if err := triageDeleteCmd.RunE(triageDeleteCmd, []string{"tr1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if len(stub.CallsFor("DELETE", "/api/v1/triage-rules/tr1")) != 1 {
			t.Error("expected 1 DELETE")
		}
	})
	t.Run("server error", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, triageDeleteCmd)
		stub.OnDelete("/api/v1/triage-rules/tr1", clitest.ErrorResponse(404, "rule not found"))
		covSetFlags(t, triageDeleteCmd, map[string]string{"yes": "true"})
		err := triageDeleteCmd.RunE(triageDeleteCmd, []string{"tr1"})
		if err == nil || !strings.Contains(err.Error(), "rule not found") {
			t.Fatalf("expected 404 error, got %v", err)
		}
	})
}

// ─── recurring ──────────────────────────────────────────────────────────

func TestRecurringListCmd_RunE(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/recurring-issues",
		clitest.JSONResponse(200, []map[string]any{{"id": "r1", "title": "weekly report"}}))
	out := covCaptureStdoutCli3(t, func() {
		if err := recurringListCmd.RunE(recurringListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "weekly report") {
		t.Errorf("schedule missing: %q", out)
	}
}

func TestRecurringCreateCmd_HappyPath(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, recurringCreateCmd)
	stub.OnPost("/api/v1/recurring-issues",
		clitest.JSONResponse(201, map[string]any{"id": "r1"}))

	covSetFlags(t, recurringCreateCmd, map[string]string{
		"crew": "crew-1", "title": "weekly", "cron": "0 9 * * 1",
		"description": "desc", "labels": `["ops"]`,
	})
	if err := recurringCreateCmd.RunE(recurringCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/recurring-issues")[0].Body, &body)
	if body["crew_id"] != "crew-1" || body["title"] != "weekly" || body["cron_expression"] != "0 9 * * 1" {
		t.Errorf("create body wrong: %v", body)
	}
	if body["description"] != "desc" || body["labels_json"] != `["ops"]` {
		t.Errorf("optional fields wrong: %v", body)
	}
}

func TestRecurringCreateCmd_BadLabels(t *testing.T) {
	covStub(t)
	covResetFlags(t, recurringCreateCmd)
	covSetFlags(t, recurringCreateCmd, map[string]string{
		"crew": "c", "title": "t", "cron": "* * * * *", "labels": "{bad",
	})
	err := recurringCreateCmd.RunE(recurringCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "must be valid JSON") {
		t.Fatalf("expected labels error, got %v", err)
	}
}

func TestRecurringUpdateCmd_HappyPath(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, recurringUpdateCmd)
	stub.OnPatch("/api/v1/recurring-issues/r1",
		clitest.JSONResponse(200, map[string]any{"id": "r1"}))

	covSetFlags(t, recurringUpdateCmd, map[string]string{
		"cron": "*/30 * * * *", "enabled": "true",
	})
	if err := recurringUpdateCmd.RunE(recurringUpdateCmd, []string{"r1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/recurring-issues/r1")[0].Body, &body)
	if body["cron_expression"] != "*/30 * * * *" {
		t.Errorf("cron wrong: %v", body)
	}
	if enabled, ok := body["enabled"].(bool); !ok || !enabled {
		t.Errorf("enabled wrong: %v", body["enabled"])
	}
}

func TestRecurringUpdateCmd_BadLabels(t *testing.T) {
	covStub(t)
	covResetFlags(t, recurringUpdateCmd)
	covSetFlags(t, recurringUpdateCmd, map[string]string{"labels": "{nope"})
	err := recurringUpdateCmd.RunE(recurringUpdateCmd, []string{"r1"})
	if err == nil || !strings.Contains(err.Error(), "must be valid JSON") {
		t.Fatalf("expected labels error, got %v", err)
	}
}

func TestRecurringDeleteCmd(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete("/api/v1/recurring-issues/r1", clitest.EmptyResponse(204))
		if err := recurringDeleteCmd.RunE(recurringDeleteCmd, []string{"r1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
		if len(stub.CallsFor("DELETE", "/api/v1/recurring-issues/r1")) != 1 {
			t.Error("expected 1 DELETE")
		}
	})
	t.Run("error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete("/api/v1/recurring-issues/r1", clitest.ErrorResponse(404, "schedule not found"))
		err := recurringDeleteCmd.RunE(recurringDeleteCmd, []string{"r1"})
		if err == nil || !strings.Contains(err.Error(), "schedule not found") {
			t.Fatalf("expected error, got %v", err)
		}
	})
}

// ─── saved-view ─────────────────────────────────────────────────────────

func TestSavedViewListCmd_RunE(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/saved-views",
		clitest.JSONResponse(200, []map[string]any{{"id": "v1", "name": "my-bugs"}}))
	out := covCaptureStdoutCli3(t, func() {
		if err := savedViewListCmd.RunE(savedViewListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "my-bugs") {
		t.Errorf("view missing: %q", out)
	}
}

func TestSavedViewCreateCmd(t *testing.T) {
	t.Run("happy with sort and shared", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, savedViewCreateCmd)
		stub.OnPost("/api/v1/saved-views", clitest.JSONResponse(201, map[string]any{"id": "v1"}))
		covSetFlags(t, savedViewCreateCmd, map[string]string{
			"name": "my-bugs", "filters": `{"status":"open"}`, "sort": `{"by":"created"}`,
			"view-type": "board", "shared": "true",
		})
		if err := savedViewCreateCmd.RunE(savedViewCreateCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
		var body map[string]any
		clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/saved-views")[0].Body, &body)
		if body["name"] != "my-bugs" || body["filters_json"] != `{"status":"open"}` {
			t.Errorf("body wrong: %v", body)
		}
		if body["sort_json"] != `{"by":"created"}` || body["view_type"] != "board" || body["shared"] != true {
			t.Errorf("optional fields wrong: %v", body)
		}
	})
	t.Run("invalid sort", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, savedViewCreateCmd)
		covSetFlags(t, savedViewCreateCmd, map[string]string{
			"name": "n", "filters": `{}`, "sort": "{bad",
		})
		err := savedViewCreateCmd.RunE(savedViewCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "--sort must be valid JSON") {
			t.Fatalf("expected sort error, got %v", err)
		}
	})
}

func TestSavedViewUpdateCmd(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, savedViewUpdateCmd)
		stub.OnPatch("/api/v1/saved-views/v1", clitest.JSONResponse(200, map[string]any{"id": "v1"}))
		covSetFlags(t, savedViewUpdateCmd, map[string]string{
			"filters": `{"a":1}`, "sort": "", "default": "true", "shared": "false",
		})
		if err := savedViewUpdateCmd.RunE(savedViewUpdateCmd, []string{"v1"}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
		var body map[string]any
		clitest.MustDecodeJSONBody(stub.CallsFor("PATCH", "/api/v1/saved-views/v1")[0].Body, &body)
		if body["filters_json"] != `{"a":1}` || body["is_default"] != true || body["shared"] != false {
			t.Errorf("body wrong: %v", body)
		}
		if v, ok := body["sort_json"]; !ok || v != "" {
			t.Errorf("explicit empty sort should be sent: %v", body)
		}
	})
	t.Run("invalid filters", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, savedViewUpdateCmd)
		covSetFlags(t, savedViewUpdateCmd, map[string]string{"filters": "{bad"})
		err := savedViewUpdateCmd.RunE(savedViewUpdateCmd, []string{"v1"})
		if err == nil || !strings.Contains(err.Error(), "--filters must be valid JSON") {
			t.Fatalf("expected filters error, got %v", err)
		}
	})
	t.Run("invalid sort", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, savedViewUpdateCmd)
		covSetFlags(t, savedViewUpdateCmd, map[string]string{"sort": "{bad"})
		err := savedViewUpdateCmd.RunE(savedViewUpdateCmd, []string{"v1"})
		if err == nil || !strings.Contains(err.Error(), "--sort must be valid JSON") {
			t.Fatalf("expected sort error, got %v", err)
		}
	})
}

func TestSavedViewDeleteCmd(t *testing.T) {
	stub := covStub(t)
	stub.OnDelete("/api/v1/saved-views/v1", clitest.EmptyResponse(204))
	if err := savedViewDeleteCmd.RunE(savedViewDeleteCmd, []string{"v1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if len(stub.CallsFor("DELETE", "/api/v1/saved-views/v1")) != 1 {
		t.Error("expected 1 DELETE")
	}
}

// ─── mcp-calls / metrics ────────────────────────────────────────────────

func TestMcpCallsCmd_RunE(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, mcpCallsCmd)
	stub.OnGet("/api/v1/mcp-tool-calls",
		clitest.JSONResponse(200, []map[string]any{{"tool": "search", "agent": "viktor"}}))

	out := covCaptureStdoutCli3(t, func() {
		if err := mcpCallsCmd.RunE(mcpCallsCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "search") {
		t.Errorf("tool call missing: %q", out)
	}
	calls := stub.CallsFor("GET", "/api/v1/mcp-tool-calls")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=50") {
		t.Errorf("expected default limit=50, got %+v", calls)
	}
}

func TestMetricsCmd_RunE(t *testing.T) {
	t.Run("mission summary default", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, metricsCmd)
		stub.OnGet("/api/v1/mission-metrics",
			clitest.JSONResponse(200, map[string]any{"success_rate": 0.93}))
		out := covCaptureStdoutCli3(t, func() {
			if err := metricsCmd.RunE(metricsCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "0.93") {
			t.Errorf("metrics missing: %q", out)
		}
	})

	t.Run("series timeseries", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, metricsCmd)
		stub.OnGet("/api/v1/metrics/timeseries",
			clitest.JSONResponse(200, map[string]any{"points": []int{1, 2}}))
		covSetFlags(t, metricsCmd, map[string]string{"series": "active_runs", "range": "1h"})
		if err := metricsCmd.RunE(metricsCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
		calls := stub.CallsFor("GET", "/api/v1/metrics/timeseries")
		if len(calls) != 1 {
			t.Fatalf("expected 1 GET, got %d", len(calls))
		}
		if !strings.Contains(calls[0].Query, "metric=active_runs") || !strings.Contains(calls[0].Query, "range=1h") {
			t.Errorf("timeseries query wrong: %q", calls[0].Query)
		}
	})
}
