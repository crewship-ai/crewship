package main

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These tests lock the --format contract for batch 1 of the sweep (issue
// #964): each migrated command routed json/yaml/ndjson through the shared
// AutoHuman/Machine helpers instead of only special-casing json. The guards
// below assert the flip side — with NO machine format the hand-crafted human
// renderer still prints (or, for Machine commands, the default stays JSON).
// covSetupCli5 blanks flagFormat so these run in the default (human) path.

// ─── routine bench ───────────────────────────────────────────────────────

func TestRoutineBenchRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/workspaces/"+covWSCli5+"/pipelines/bench-slug/run",
		clitest.JSONResponse(200, map[string]any{
			"run_id": "r1", "status": "COMPLETED", "duration_ms": 120, "cost_usd": 0.01,
		}))
	// Keep the bench to a single POST so the test isn't 10 round-trips.
	covSetFlagCli5(t, routineBenchCmd, "runs", "1")

	var err error
	out := covCaptureStdoutCli5(t, func() { err = routineBenchCmd.RunE(routineBenchCmd, []string{"bench-slug"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Pass rate:", "Verdict:"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── routine doctor ──────────────────────────────────────────────────────

func TestRoutineDoctorRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	// A 404 on the routine lookup short-circuits to a single FAIL check —
	// the cheapest path that still exercises the human table renderer.
	stub.OnGet("/api/v1/workspaces/"+covWSCli5+"/pipelines/ghost-slug",
		clitest.ErrorResponse(404, "no such routine"))

	var out string
	// A FAILed check legitimately returns a non-zero error; we only care
	// that the human report rendered rather than machine JSON.
	out = covCaptureStdoutCli5(t, func() { _ = runRoutineDoctor(routineDoctorCmd, []string{"ghost-slug"}) })
	for _, want := range []string{"Doctor:", "not found"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── agent logs (AutoHuman) ──────────────────────────────────────────────

func TestAgentLogsRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	const agentID = "c0000000000000000000001" // CUID-shaped → skips slug resolve
	stub.OnGet("/api/v1/agents/"+agentID+"/logs", clitest.JSONResponse(200, map[string]any{
		"logs": "boot line one\nboot line two\n",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = agentLogsCmd.RunE(agentLogsCmd, []string{agentID}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "boot line one") {
		t.Errorf("human log output missing raw logs; got:\n%s", out)
	}
}

// ─── agent debug (Machine — default stays JSON) ──────────────────────────

func TestAgentDebugRunE_DefaultStaysJSON(t *testing.T) {
	stub := covSetupCli5(t)
	const agentID = "c0000000000000000000002"
	stub.OnGet("/api/v1/agents/"+agentID+"/debug", clitest.JSONResponse(200, map[string]any{
		"container_state": "running", "crewshipd": "ok",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = agentDebugCmd.RunE(agentDebugCmd, []string{agentID}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Machine's canonical default is JSON — stdout must parse.
	var payload map[string]any
	if uerr := json.Unmarshal([]byte(out), &payload); uerr != nil {
		t.Fatalf("default stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	if payload["container_state"] != "running" {
		t.Errorf("debug payload mismatch: %+v", payload)
	}
}

// TestAgentDebugRunE_YAMLAndNDJSON is the flip side of DefaultStaysJSON:
// agentDebugCmd routes through f.Machine now, so --format yaml/ndjson must
// carry the payload rather than silently falling back to JSON.
func TestAgentDebugRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	const agentID = "c0000000000000000000002"
	stub.OnGet("/api/v1/agents/"+agentID+"/debug", clitest.JSONResponse(200, map[string]any{
		"container_state": "running", "crewshipd": "ok",
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = agentDebugCmd.RunE(agentDebugCmd, []string{agentID}) })
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
	if yamlPayload["container_state"] != "running" {
		t.Errorf("yaml payload mismatch: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = agentDebugCmd.RunE(agentDebugCmd, []string{agentID}) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for a single object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["container_state"] != "running" {
		t.Errorf("ndjson payload mismatch: %+v", ndjsonPayload)
	}
}

// ─── consolidate run ─────────────────────────────────────────────────────

func TestConsolidateRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/consolidate/run", clitest.JSONResponse(200, map[string]any{
		"triggered": true, "worker_id": "w-123",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = consolidateRunCmd.RunE(consolidateRunCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Consolidation triggered", "w-123"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// ─── cost ────────────────────────────────────────────────────────────────

func TestCostRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []topSpenderRow{{ScopeKind: "crew", ScopeID: "backend", CostUSD: 1.5, CallCount: 4}},
	}))
	stub.OnGet("/api/v1/paymaster/spend/by-crew", clitest.JSONResponse(200, map[string]any{
		"rows": []crewSpendRow{{CrewID: "backend", CostUSD: 1.5, CallCount: 4, InTokens: 100, OutTokens: 50}},
	}))
	stub.OnGet("/api/v1/paymaster/subscriptions", clitest.JSONResponse(200, map[string]any{
		"rows": []subUsageRow{},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = costCmd.RunE(costCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Cost summary", "By crew"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}

// TestCostRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman: costCmd
// routes through f.AutoHuman now, so --format yaml/ndjson must carry the
// {range, top, crews, subscriptions} payload instead of the hand-rolled
// section printers.
func TestCostRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/paymaster/top-spenders", clitest.JSONResponse(200, map[string]any{
		"rows": []topSpenderRow{{ScopeKind: "crew", ScopeID: "backend", CostUSD: 1.5, CallCount: 4}},
	}))
	stub.OnGet("/api/v1/paymaster/spend/by-crew", clitest.JSONResponse(200, map[string]any{
		"rows": []crewSpendRow{{CrewID: "backend", CostUSD: 1.5, CallCount: 4, InTokens: 100, OutTokens: 50}},
	}))
	stub.OnGet("/api/v1/paymaster/subscriptions", clitest.JSONResponse(200, map[string]any{
		"rows": []subUsageRow{},
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = costCmd.RunE(costCmd, nil) })
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
	if yamlPayload["range"] != "24h" {
		t.Errorf("yaml payload missing range=24h: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = costCmd.RunE(costCmd, nil) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	// costCmd's payload is a single map (not a top-level slice), so NDJSON
	// still emits exactly one line for the whole object.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for the cost summary object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["range"] != "24h" {
		t.Errorf("ndjson payload missing range=24h: %+v", ndjsonPayload)
	}
}

// ─── crew standup ────────────────────────────────────────────────────────

func TestCrewStandupRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	const crewID = "c0000000000000000000003" // CUID-shaped → skips slug resolve
	stub.OnGet("/api/v1/crews/"+crewID+"/standup", clitest.JSONResponse(200, map[string]any{
		"standup": "Yesterday: shipped the format sweep.",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = crewStandupCmd.RunE(crewStandupCmd, []string{crewID}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Yesterday: shipped the format sweep.") {
		t.Errorf("human standup text missing; got:\n%s", out)
	}
}

// TestCrewStandupRunE_YAMLAndNDJSON is the flip side of DefaultStaysHuman:
// crewStandupCmd routes through f.AutoHuman now, so --format yaml/ndjson
// must carry the raw {standup} payload instead of the plain-text println.
func TestCrewStandupRunE_YAMLAndNDJSON(t *testing.T) {
	stub := covSetupCli5(t)
	const crewID = "c0000000000000000000003" // CUID-shaped → skips slug resolve
	stub.OnGet("/api/v1/crews/"+crewID+"/standup", clitest.JSONResponse(200, map[string]any{
		"standup": "Yesterday: shipped the format sweep.",
	}))

	flagFormat = "yaml"
	var err error
	out := covCaptureStdoutCli5(t, func() { err = crewStandupCmd.RunE(crewStandupCmd, []string{crewID}) })
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
	if yamlPayload["standup"] != "Yesterday: shipped the format sweep." {
		t.Errorf("yaml payload mismatch: %+v", yamlPayload)
	}

	flagFormat = "ndjson"
	out = covCaptureStdoutCli5(t, func() { err = crewStandupCmd.RunE(crewStandupCmd, []string{crewID}) })
	if err != nil {
		t.Fatalf("ndjson RunE: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("ndjson: want exactly 1 line for a single object, got %d:\n%s", len(lines), out)
	}
	var ndjsonPayload map[string]any
	if uerr := json.Unmarshal([]byte(lines[0]), &ndjsonPayload); uerr != nil {
		t.Fatalf("ndjson line is not valid JSON: %v\nline:\n%s", uerr, lines[0])
	}
	if ndjsonPayload["standup"] != "Yesterday: shipped the format sweep." {
		t.Errorf("ndjson payload mismatch: %+v", ndjsonPayload)
	}
}

// ─── conversation search ─────────────────────────────────────────────────

func TestConversationSearchRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	const agentID = "c0000000000000000000004"
	stub.OnPost("/api/v1/conversations/search", clitest.JSONResponse(200, map[string]any{
		"count": 1,
		"query": "deploy",
		"hits": []map[string]any{
			{"id": "m1", "session_id": "s1", "role": "user", "content": "deploy pipeline please", "ts": "2026-07-13T00:00:00Z"},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = conversationSearchCmd.RunE(conversationSearchCmd, []string{agentID, "deploy"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"match(es) for", "deploy pipeline please"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}
