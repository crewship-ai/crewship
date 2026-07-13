package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

// TestPaymasterTopRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// paymasterTopCmd routes body.Rows (a slice) through f.AutoHuman now, so
// --format yaml/ndjson must carry the top-spender rows instead of the
// ranked human list.
func TestPaymasterTopRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"scope_kind": "crew", "scope_id": "backend", "cost_usd": 9.99, "call_count": 42},
			{"scope_kind": "agent", "scope_id": "viktor", "cost_usd": 1.23, "call_count": 5},
		},
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = paymasterTopCmd.RunE(paymasterTopCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["scopekind"] != "crew" || yamlRows[1]["scopekind"] != "agent" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = paymasterTopCmd.RunE(paymasterTopCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per top-spender row (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["scope_kind"] != "crew" || second["scope_kind"] != "agent" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
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

// TestApprovalsListRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// approvalsListCmd routes body.Rows (a slice) through f.AutoHuman now, so
// --format yaml/ndjson must carry the approval rows instead of the colored
// queue table.
func TestApprovalsListRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/approvals", clitest.JSONResponse(200, map[string]any{
		"rows": []map[string]any{
			{"id": "apr_batch5list", "crew_id": "backend", "agent_id": "viktor", "kind": "deploy", "reason": "ship it", "status": "pending"},
			{"id": "apr_batch5list2", "crew_id": "backend", "agent_id": "eva", "kind": "shell.exec", "reason": "cleanup", "status": "approved"},
		},
		"count": 2,
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = approvalsListCmd.RunE(approvalsListCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlRows []map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlRows); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if len(yamlRows) != 2 {
		t.Fatalf("yaml: want 2 approval rows, got %d; out:\n%s", len(yamlRows), out)
	}
	if yamlRows[0]["kind"] != "deploy" || yamlRows[1]["kind"] != "shell.exec" {
		t.Errorf("yaml rows mismatch: %+v", yamlRows)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = approvalsListCmd.RunE(approvalsListCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson: want one line per approval row (2), got %d:\n%s", len(lines), out)
	}
	var first, second map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &first); uerr != nil {
		t.Fatalf("ndjson line 0 is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if uerr := json.Unmarshal([]byte(lines[1]), &second); uerr != nil {
		t.Fatalf("ndjson line 1 is not valid JSON: %v\nline:\n%s", uerr, lines[1])
	}
	if first["kind"] != "deploy" || second["kind"] != "shell.exec" {
		t.Errorf("ndjson rows mismatch: %+v / %+v", first, second)
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

// TestRunInsightsRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// runInsightsCmd routes the single insights struct through f.AutoHuman now,
// so --format yaml/ndjson must carry the {window, totals, duration,
// by_trigger, ...} payload instead of the rendered report. Unlike the
// paymaster/approvals list commands, this payload is a single object —
// ndjson must emit exactly one line.
func TestRunInsightsRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/runs/insights", clitest.JSONResponse(200, map[string]any{
		"window":   "24h",
		"totals":   map[string]any{"total": 5, "succeeded": 4, "failed": 1, "running": 0},
		"duration": map[string]any{"p50_ms": 1200, "p95_ms": 3400},
		"by_trigger": []map[string]any{
			{"key": "manual", "total": 5, "failed": 1},
		},
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = runInsightsCmd.RunE(runInsightsCmd, nil) })
	if err != nil {
		t.Fatalf("yaml RunE: %v", err)
	}
	var yamlPayload map[string]any
	if uerr := yaml.Unmarshal([]byte(out), &yamlPayload); uerr != nil {
		t.Fatalf("yaml stdout does not parse: %v\ngot:\n%s", uerr, out)
	}
	if yamlPayload == nil {
		t.Fatalf("yaml stdout parsed to nil; got:\n%s", out)
	}
	if yamlPayload["window"] != "24h" {
		t.Errorf("yaml payload missing window=24h: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = runInsightsCmd.RunE(runInsightsCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// The insights payload is a single struct (not a top-level slice), so
	// NDJSON emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the insights object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["window"] != "24h" {
		t.Errorf("ndjson payload missing window=24h: %+v", ndjsonPayload)
	}
}
