package main

import (
	"strings"
	"testing"
)

func sampleReport() reportData {
	return reportData{
		RoutineName: "reconcile-invoices",
		RunID:       "run_abc",
		Status:      "completed",
		Inputs:      map[string]any{"month": "June", "n": 3},
		Steps: []reportStep{
			{ID: "parse", Status: "completed", DurationMs: 1200, CostUSD: 0.002, Output: `{"rows":42}`},
			{ID: "verify", Status: "completed", DurationMs: 800, CostUSD: 0.001, Output: "all balanced"},
		},
		FinalOutput:     "Reconciled 42 rows for June; all balanced.",
		TotalCostUSD:    0.003,
		TotalDurationMs: 2000,
	}
}

func TestBuildReport_Markdown(t *testing.T) {
	md := buildReport(sampleReport(), "md")
	for _, want := range []string{
		"reconcile-invoices", // routine name
		"June",               // input value
		"parse",              // step name
		"verify",
		"all balanced",       // step output
		"Reconciled 42 rows", // final output
		"$0.0030",            // total cost
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown report missing %q:\n%s", want, md)
		}
	}
	// Markdown, not HTML.
	if strings.Contains(md, "<html") || strings.Contains(md, "<td>") {
		t.Errorf("md format leaked HTML tags:\n%s", md)
	}
}

func TestBuildReport_HTML(t *testing.T) {
	html := buildReport(sampleReport(), "html")
	if !strings.Contains(html, "<!doctype html>") && !strings.Contains(html, "<!DOCTYPE html>") {
		t.Errorf("html report missing doctype:\n%s", html[:min(200, len(html))])
	}
	for _, want := range []string{"reconcile-invoices", "parse", "Reconciled 42 rows", "$0.0030"} {
		if !strings.Contains(html, want) {
			t.Errorf("html report missing %q", want)
		}
	}
	// Output containing HTML metacharacters must be escaped, not injected.
	dangerous := sampleReport()
	dangerous.FinalOutput = `<script>alert(1)</script>`
	out := buildReport(dangerous, "html")
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("html report did not escape output — XSS risk:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("html report should escape < > in output")
	}
}

func TestBuildReport_ClientRedactsInternalNoise(t *testing.T) {
	d := sampleReport()
	d.Client = true
	for _, format := range []string{"md", "html"} {
		out := buildReport(d, format)
		// The deliverable + step names stay; internal noise goes.
		if !strings.Contains(out, "Reconciled 42 rows") || !strings.Contains(out, "parse") {
			t.Errorf("%s client report dropped content:\n%s", format, out)
		}
		for _, noise := range []string{"run_abc", "$0.0030", "$0.0020"} {
			if strings.Contains(out, noise) {
				t.Errorf("%s client report leaked internal noise %q:\n%s", format, noise, out)
			}
		}
		// Plain-word status, not the raw enum.
		if !strings.Contains(out, "Succeeded") {
			t.Errorf("%s client report should say Succeeded:\n%s", format, out)
		}
	}
}

func TestReportStepsFromEvents_OrdersAndAttachesOutput(t *testing.T) {
	// DESC from the server (newest first) — the builder reverses to exec order.
	rows := []watchEntry{
		ev("r", "pipeline.step.completed", "verify", "2026-07-07T12:00:20Z", map[string]any{"cost_usd": 0.001, "duration_ms": 800}),
		ev("r", "pipeline.step.started", "verify", "2026-07-07T12:00:11Z", nil),
		ev("r", "pipeline.step.completed", "parse", "2026-07-07T12:00:10Z", map[string]any{"cost_usd": 0.002}),
		ev("r", "pipeline.step.started", "parse", "2026-07-07T12:00:01Z", nil),
	}
	steps := reportStepsFromEvents(rows, "r", map[string]string{"parse": "42 rows", "verify": "balanced"})
	if len(steps) != 2 || steps[0].ID != "parse" || steps[1].ID != "verify" {
		t.Fatalf("step order = %+v, want parse then verify", steps)
	}
	if steps[0].Output != "42 rows" || steps[1].Status != "completed" || steps[1].CostUSD != 0.001 {
		t.Errorf("step data = %+v", steps)
	}
}

func TestReportStepsFromEvents_SkippedAndRetry(t *testing.T) {
	// Newest-first (server DESC). A skipped step (dedicated type) must land
	// as "skipped", not stall on "running"; a retry breadcrumb must NOT mark
	// its step failed — the step's real completed event wins.
	rows := []watchEntry{
		ev("r", "pipeline.step.completed", "fetch", "2026-07-07T12:00:30Z", map[string]any{"cost_usd": 0.002}),
		ev("r", "pipeline.step.retrying", "fetch", "2026-07-07T12:00:20Z", map[string]any{"kind": "retry", "attempt": 1.0, "max": 3.0}),
		ev("r", "pipeline.step.started", "fetch", "2026-07-07T12:00:11Z", nil),
		ev("r", "pipeline.step.skipped", "notify", "2026-07-07T12:00:10Z", map[string]any{"kind": "skipped", "condition": "{{ inputs.dry }}"}),
		ev("r", "pipeline.step.started", "notify", "2026-07-07T12:00:01Z", nil),
	}
	steps := reportStepsFromEvents(rows, "r", nil)
	byID := map[string]string{}
	for _, s := range steps {
		byID[s.ID] = s.Status
	}
	if byID["notify"] != "skipped" {
		t.Errorf("skipped step status = %q, want skipped", byID["notify"])
	}
	if byID["fetch"] != "completed" {
		t.Errorf("retried-then-completed step status = %q, want completed (retry must not mark it failed)", byID["fetch"])
	}
}

func TestReportStepsFromEvents_LegacyKindSkippedMarker(t *testing.T) {
	// Pre-dedicated-type rows: skipped arrived as completed+kind=skipped.
	// The kind fallback must still render it as skipped, not completed.
	rows := []watchEntry{
		ev("r", "pipeline.step.completed", "notify", "2026-07-07T12:00:10Z", map[string]any{"kind": "skipped", "condition": "false"}),
		ev("r", "pipeline.step.started", "notify", "2026-07-07T12:00:01Z", nil),
	}
	steps := reportStepsFromEvents(rows, "r", nil)
	if len(steps) != 1 || steps[0].Status != "skipped" {
		t.Fatalf("legacy skipped marker not honoured: %+v", steps)
	}
}

func TestBuildReport_FailedRunShowsError(t *testing.T) {
	d := sampleReport()
	d.Status = "failed"
	d.Error = "step verify timed out"
	d.Steps[1].Status = "failed"
	md := buildReport(d, "md")
	if !strings.Contains(strings.ToLower(md), "failed") || !strings.Contains(md, "step verify timed out") {
		t.Errorf("failed report missing status/error:\n%s", md)
	}
}
