package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covMissionID = "cmission000000000000000"

// ─── printSpendTable ─────────────────────────────────────────────────────

func TestPrintSpendTable_CrewRows(t *testing.T) {
	covSetupCli5(t)
	rows := []crewSpendRow{
		{CrewID: "backend", CostUSD: 1.2345, CallCount: 10, InTokens: 700, OutTokens: 300},
	}
	var err error
	out := covCaptureStdoutCli5(t, func() { err = printSpendTable("Crew", rows) })
	if err != nil {
		t.Fatalf("printSpendTable: %v", err)
	}
	for _, want := range []string{"Crew", "backend", "$  1.2345", "10", "1000"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q; got:\n%s", want, out)
		}
	}
}

func TestPrintSpendTable_AgentRows(t *testing.T) {
	covSetupCli5(t)
	rows := []agentSpendRow{
		{AgentID: "viktor", CostUSD: 0.5, CallCount: 3, InTokens: 80, OutTokens: 20},
	}
	var err error
	out := covCaptureStdoutCli5(t, func() { err = printSpendTable("Agent", rows) })
	if err != nil {
		t.Fatalf("printSpendTable: %v", err)
	}
	if !strings.Contains(out, "viktor") || !strings.Contains(out, "100") {
		t.Errorf("agent table wrong; got:\n%s", out)
	}
}

func TestPrintSpendTable_UnsupportedType(t *testing.T) {
	covSetupCli5(t)
	var err error
	covCaptureStdoutCli5(t, func() { err = printSpendTable("X", []string{"nope"}) })
	if err == nil || !strings.Contains(err.Error(), "unsupported rows type") {
		t.Errorf("expected unsupported-type error; got %v", err)
	}
}

func TestPrintSpendTable_JSONFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "json"
	rows := []crewSpendRow{{CrewID: "x", CostUSD: 2}}
	var err error
	out := covCaptureStdoutCli5(t, func() { err = printSpendTable("Crew", rows) })
	if err != nil {
		t.Fatalf("printSpendTable json: %v", err)
	}
	var decoded []crewSpendRow
	if jsonErr := json.Unmarshal([]byte(out), &decoded); jsonErr != nil {
		t.Fatalf("not JSON: %v\n%s", jsonErr, out)
	}
	if len(decoded) != 1 || decoded[0].CrewID != "x" || decoded[0].CostUSD != 2 {
		t.Errorf("decoded = %v", decoded)
	}
}

// ─── by-crew ─────────────────────────────────────────────────────────────

func TestPaymasterByCrewRunE(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/spend/by-crew", clitest.JSONResponse(200, map[string]any{
		"rows": []crewSpendRow{{CrewID: "backend", CostUSD: 3.5, CallCount: 7, InTokens: 100, OutTokens: 50}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = paymasterByCrewCmd.RunE(paymasterByCrewCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "backend") || !strings.Contains(out, "3.5000") {
		t.Errorf("output missing spend row; got:\n%s", out)
	}
	calls := stub.CallsFor("GET", "/api/v1/paymaster/spend/by-crew")
	if len(calls) != 1 {
		t.Fatalf("expected 1 GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "range=7d") {
		t.Errorf("default range missing: %q", calls[0].Query)
	}
}

func TestPaymasterByCrewRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/spend/by-crew", clitest.ErrorResponse(500, "ledger locked"))

	err := paymasterByCrewCmd.RunE(paymasterByCrewCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "ledger locked") {
		t.Errorf("expected API error; got %v", err)
	}
}

func TestPaymasterByCrewRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := paymasterByCrewCmd.RunE(paymasterByCrewCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

// ─── by-agent ────────────────────────────────────────────────────────────

func TestPaymasterByAgentRunE(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, paymasterByAgentCmd, "range", "24h")
	stub.OnGet("/api/v1/paymaster/spend/by-agent/"+covCrewIDCli5, clitest.JSONResponse(200, map[string]any{
		"rows": []agentSpendRow{{AgentID: "viktor", CostUSD: 0.9, CallCount: 2, InTokens: 10, OutTokens: 5}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterByAgentCmd.RunE(paymasterByAgentCmd, []string{covCrewIDCli5})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "viktor") {
		t.Errorf("output missing agent row; got:\n%s", out)
	}
	calls := stub.CallsFor("GET", "/api/v1/paymaster/spend/by-agent/"+covCrewIDCli5)
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "range=24h") {
		t.Errorf("by-agent call wrong: %+v", calls)
	}
}

func TestPaymasterByAgentRunE_UnknownCrew(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := paymasterByAgentCmd.RunE(paymasterByAgentCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

// ─── top ─────────────────────────────────────────────────────────────────

func TestPaymasterTopRunE_Table(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"scope_kind": "crew", "scope_id": "backend", "cost_usd": 9.99, "call_count": 42},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = paymasterTopCmd.RunE(paymasterTopCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"crew/backend", "9.9900", "42 calls"} {
		if !strings.Contains(out, want) {
			t.Errorf("top table missing %q; got:\n%s", want, out)
		}
	}
	q := stub.CallsFor("GET", "/api/v1/paymaster/top-spenders")[0].Query
	if !strings.Contains(q, "limit=10") || !strings.Contains(q, "range=7d") {
		t.Errorf("top query wrong: %q", q)
	}
}

func TestPaymasterTopRunE_JSON(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"scope_kind": "agent", "scope_id": "viktor", "cost_usd": 1, "call_count": 1}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = paymasterTopCmd.RunE(paymasterTopCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var rows []map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &rows); jsonErr != nil {
		t.Fatalf("not JSON: %v\n%s", jsonErr, out)
	}
	if len(rows) != 1 || rows[0]["scope_id"] != "viktor" {
		t.Errorf("rows = %v", rows)
	}
}

// ─── by-mission ──────────────────────────────────────────────────────────

func TestPaymasterByMissionRunE_Table(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/spend/by-mission/"+covMissionID, clitest.JSONResponse(200, map[string]any{
		"mission_id": covMissionID,
		"row": map[string]any{
			"mission_id": covMissionID, "cost_usd": 0.1234,
			"call_count": 6, "input_tokens": 600, "output_tokens": 400,
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterByMissionCmd.RunE(paymasterByMissionCmd, []string{covMissionID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Mission " + covMissionID, "$0.1234", "Calls:        6", "Total tokens: 1000"} {
		if !strings.Contains(out, want) {
			t.Errorf("by-mission output missing %q; got:\n%s", want, out)
		}
	}
}

func TestPaymasterByMissionRunE_NotFound(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/spend/by-mission/ghost", clitest.ErrorResponse(404, "mission not found"))

	err := paymasterByMissionCmd.RunE(paymasterByMissionCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "mission not found") {
		t.Errorf("expected 404; got %v", err)
	}
}

// ─── subscriptions ───────────────────────────────────────────────────────

func TestPaymasterSubscriptionsRunE_Rows(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, paymasterSubscriptionsCmd, "range", "30d")
	covSetFlagCli5(t, paymasterSubscriptionsCmd, "since", "2026-06-01T00:00:00Z")
	covSetFlagCli5(t, paymasterSubscriptionsCmd, "until", "2026-06-12T00:00:00Z")
	stub.OnGet("/api/v1/paymaster/subscriptions", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{
			"plan": "max-20x", "provider": "ANTHROPIC", "call_count": 12,
			"input_tokens": 900, "output_tokens": 100, "last_used_at": "2026-06-11T22:00:00Z",
		}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterSubscriptionsCmd.RunE(paymasterSubscriptionsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"max-20x", "ANTHROPIC", "12", "1000", "2026-06-11T22:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("subscriptions table missing %q; got:\n%s", want, out)
		}
	}
	q := stub.CallsFor("GET", "/api/v1/paymaster/subscriptions")[0].Query
	for _, want := range []string{"range=30d", "since=", "until="} {
		if !strings.Contains(q, want) {
			t.Errorf("subscriptions query missing %q: %q", want, q)
		}
	}
}

func TestPaymasterSubscriptionsRunE_Empty(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/subscriptions",
		clitest.JSONResponse(200, map[string]any{"rows": []map[string]any{}}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterSubscriptionsCmd.RunE(paymasterSubscriptionsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "no subscription credentials configured") {
		t.Errorf("expected empty-state hint; got:\n%s", out)
	}
}

func TestPaymasterSubscriptionsRunE_JSON(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/paymaster/subscriptions", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"plan": "pro", "provider": "ANTHROPIC", "call_count": 1}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterSubscriptionsCmd.RunE(paymasterSubscriptionsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var rows []map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &rows); jsonErr != nil {
		t.Fatalf("not JSON: %v\n%s", jsonErr, out)
	}
	if len(rows) != 1 || rows[0]["plan"] != "pro" {
		t.Errorf("rows = %v", rows)
	}
}

// ─── error + yaml branches round 2 ───────────────────────────────────────

func TestPrintSpendTable_YAMLFormat(t *testing.T) {
	covSetupCli5(t)
	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = printSpendTable("Crew", []crewSpendRow{{CrewID: "y", CostUSD: 1}})
	})
	if err != nil {
		t.Fatalf("printSpendTable yaml: %v", err)
	}
	if !strings.Contains(out, `crewid: "y"`) {
		t.Errorf("yaml output missing crew id; got:\n%s", out)
	}
}

// TestPaymasterRunE_SharedErrorBranches table-drives the no-workspace /
// transport-error / malformed-response triple across every paymaster
// subcommand — the same three branches repeat in each RunE.
func TestPaymasterRunE_SharedErrorBranches(t *testing.T) {
	type cmdCase struct {
		name string
		path string
		run  func() error
	}
	cases := []cmdCase{
		{"by-crew", "/api/v1/paymaster/spend/by-crew",
			func() error { return paymasterByCrewCmd.RunE(paymasterByCrewCmd, nil) }},
		{"by-agent", "/api/v1/paymaster/spend/by-agent/" + covCrewIDCli5,
			func() error { return paymasterByAgentCmd.RunE(paymasterByAgentCmd, []string{covCrewIDCli5}) }},
		{"top", "/api/v1/paymaster/top-spenders",
			func() error { return paymasterTopCmd.RunE(paymasterTopCmd, nil) }},
		{"by-mission", "/api/v1/paymaster/spend/by-mission/" + covMissionID,
			func() error { return paymasterByMissionCmd.RunE(paymasterByMissionCmd, []string{covMissionID}) }},
		{"subscriptions", "/api/v1/paymaster/subscriptions",
			func() error { return paymasterSubscriptionsCmd.RunE(paymasterSubscriptionsCmd, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/no_workspace", func(t *testing.T) {
			covSetupCli5(t)
			cliCfg = &cli.CLIConfig{Token: "fake-token"}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
				t.Errorf("expected workspace error; got %v", err)
			}
		})
		t.Run(tc.name+"/transport_error", func(t *testing.T) {
			stub := covSetupCli5(t)
			stub.Close()
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "request failed") {
				t.Errorf("expected transport error; got %v", err)
			}
		})
		t.Run(tc.name+"/malformed_response", func(t *testing.T) {
			stub := covSetupCli5(t)
			stub.OnGet(tc.path, clitest.TextResponse(200, "not json"))
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "decode response") {
				t.Errorf("expected decode error; got %v", err)
			}
		})
	}
}

func TestPaymasterTopRunE_YAML(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "yaml"
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"scope_kind": "crew", "scope_id": "b", "cost_usd": 1, "call_count": 2}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = paymasterTopCmd.RunE(paymasterTopCmd, nil) })
	if err != nil {
		t.Fatalf("RunE yaml: %v", err)
	}
	if !strings.Contains(out, "scopeid: b") {
		t.Errorf("yaml output missing row; got:\n%s", out)
	}
}

func TestPaymasterByMissionRunE_JSONAndYAML(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/spend/by-mission/"+covMissionID, clitest.JSONResponse(200, map[string]any{
		"mission_id": covMissionID,
		"row":        map[string]any{"mission_id": covMissionID, "cost_usd": 1.5, "call_count": 2},
	}))

	flagFormat = "json"
	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterByMissionCmd.RunE(paymasterByMissionCmd, []string{covMissionID})
	})
	if err != nil {
		t.Fatalf("RunE json: %v", err)
	}
	var v map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &v); jsonErr != nil {
		t.Fatalf("not JSON: %v\n%s", jsonErr, out)
	}
	if v["mission_id"] != covMissionID {
		t.Errorf("mission_id = %v", v["mission_id"])
	}

	flagFormat = "yaml"
	out = covCaptureStdoutCli5(t, func() {
		err = paymasterByMissionCmd.RunE(paymasterByMissionCmd, []string{covMissionID})
	})
	if err != nil {
		t.Fatalf("RunE yaml: %v", err)
	}
	if !strings.Contains(out, "missionid: "+covMissionID) {
		t.Errorf("yaml output missing mission id; got:\n%s", out)
	}
}

func TestPaymasterSubscriptionsRunE_YAML(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "yaml"
	stub.OnGet("/api/v1/paymaster/subscriptions", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{"plan": "pro", "provider": "ANTHROPIC"}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterSubscriptionsCmd.RunE(paymasterSubscriptionsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE yaml: %v", err)
	}
	if !strings.Contains(out, "plan: pro") {
		t.Errorf("yaml output missing plan; got:\n%s", out)
	}
}
