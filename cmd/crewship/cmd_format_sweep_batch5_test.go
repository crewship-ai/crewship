package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// Batch 5 of the --format contract sweep (#964). Each migrated command now
// routes json/yaml/ndjson through the structured renderers via
// Formatter.AutoHuman while keeping the hand-crafted human view for the
// default/table path. These guards lock the flip side: without a machine
// format, the distinctive human substrings must survive byte-for-byte so
// eyeballs and scrapers that read the default output don't regress. Modeled
// on TestSystemInfoRunE_DefaultStaysHuman. No ANSI is asserted — only plain
// text that sits outside color escapes.

// ─── paymaster by-crew (also covers by-agent via printSpendTable) ─────────

func TestPaymasterByCrewRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/spend/by-crew", clitest.JSONResponse(200, map[string]any{
		"rows": []crewSpendRow{{CrewID: "backend", CostUSD: 3.5, CallCount: 7, InTokens: 100, OutTokens: 50}},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = paymasterByCrewCmd.RunE(paymasterByCrewCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Cost (USD)", "backend", "3.5000"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── paymaster top ────────────────────────────────────────────────────────

func TestPaymasterTopRunE_DefaultStaysHuman(t *testing.T) {
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
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── paymaster by-mission ─────────────────────────────────────────────────

func TestPaymasterByMissionRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	const mid = "cmission000000000000001"
	stub.OnGet("/api/v1/paymaster/spend/by-mission/"+mid, clitest.JSONResponse(200, map[string]any{
		"mission_id": mid,
		"row": map[string]any{
			"mission_id": mid, "cost_usd": 0.1234,
			"call_count": 6, "input_tokens": 600, "output_tokens": 400,
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = paymasterByMissionCmd.RunE(paymasterByMissionCmd, []string{mid})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Mission " + mid, "Calls:        6", "Total tokens: 1000"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── paymaster subscriptions ──────────────────────────────────────────────

func TestPaymasterSubscriptionsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
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
	for _, want := range []string{"Plan", "max-20x", "ANTHROPIC"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── approvals get ────────────────────────────────────────────────────────

func TestApprovalsGetRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/approvals/apr_batch5", clitest.JSONResponse(200, map[string]any{
		"id": "apr_batch5", "status": "pending", "kind": "shell.exec",
		"reason": "deploy prod",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = approvalsGetCmd.RunE(approvalsGetCmd, []string{"apr_batch5"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"apr_batch5", "pending", "shell.exec"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── approvals list ───────────────────────────────────────────────────────

func TestApprovalsListRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/approvals", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{{
			"id": "apr_batch5list", "crew_id": "backend", "agent_id": "viktor",
			"kind": "deploy", "reason": "ship it", "status": "pending",
		}},
		"count": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = approvalsListCmd.RunE(approvalsListCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"apr_batch5list", "deploy", "ship it"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── run insights ─────────────────────────────────────────────────────────

func TestRunInsightsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/runs/insights", clitest.JSONResponse(200, map[string]any{
		"window":   "24h",
		"totals":   map[string]any{"total": 5, "succeeded": 4, "failed": 1, "running": 0},
		"duration": map[string]any{"p50_ms": 1200, "p95_ms": 3400},
		"by_trigger": []map[string]any{
			{"key": "manual", "total": 5, "failed": 1},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = runInsightsCmd.RunE(runInsightsCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Fleet operations · last 24h", "By trigger", "manual"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}
