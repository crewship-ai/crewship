package main

// Routine report subcommand (#840). Turns a finished run into a shareable
// deliverable a non-engineer can read: inputs → step-by-step (name / status /
// output preview) → final output → cost & duration. Markdown by default (paste
// into a ticket / chat) or self-contained HTML (--format html, e.g. > run.html
// to hand to a client). Distinct from `routine result` (just the final output)
// and `routine logs` (operator event log). Assembled from the run detail + its
// journal timeline.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type reportStep struct {
	ID         string
	Status     string
	DurationMs int64
	CostUSD    float64
	Output     string
}

type reportData struct {
	RoutineName     string
	RunID           string
	Status          string
	Inputs          map[string]any
	Steps           []reportStep
	FinalOutput     string
	TotalCostUSD    float64
	TotalDurationMs int64
	Error           string
	// Client, when true, suppresses the internal/operator fields (run id,
	// per-step cost/duration, total cost) for a customer-facing view (#840).
	Client bool
}

var (
	reportOutFile string
	reportClient  bool
)

var routineReportCmd = &cobra.Command{
	Use:   "report <run_id>",
	Short: "Build a shareable report of a run — inputs, steps, output, cost (Markdown or HTML)",
	Long: `Assemble a client-readable report of a finished run: inputs → each step's
outcome and output → the final deliverable → cost & duration.

  crewship routine report run_abc123                    # Markdown to stdout
  crewship routine report run_abc123 -f html -o run.html
  crewship routine report run_abc123 --client -o run.html   # redacted (no cost/tier/run-id)

Markdown is the default (paste into a ticket / chat); HTML is a
self-contained page you can hand to a customer. --client drops the
internal cost / run-id / per-step metadata for a customer-facing view.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		runID := args[0]
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		data, err := gatherReport(client, cmd.Context(), runID, reportClient)
		if err != nil {
			return err
		}

		format := resolvedFormat(cmd)
		if format != "html" {
			format = "md"
		}
		out := buildReport(data, format)

		if reportOutFile != "" {
			if err := os.WriteFile(reportOutFile, []byte(out), 0o644); err != nil {
				return fmt.Errorf("write %q: %w", reportOutFile, err)
			}
			fmt.Fprintf(os.Stderr, "Wrote %s report to %s\n", format, reportOutFile)
			return nil
		}
		fmt.Println(out)
		return nil
	},
}

// buildReport renders the report as Markdown ("md") or a self-contained HTML
// page ("html"). Pure — all I/O is done by the caller.
func buildReport(d reportData, format string) string {
	if format == "html" {
		return buildReportHTML(d)
	}
	return buildReportMarkdown(d)
}

func buildReportMarkdown(d reportData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", d.RoutineName)

	// Header line — status always; cost/duration only for the operator view.
	fmt.Fprintf(&b, "**Status:** %s", statusWord(d.Status))
	if !d.Client {
		fmt.Fprintf(&b, " · **Cost:** %s · **Duration:** %s · **Run:** `%s`",
			costOrDash(d.TotalCostUSD), durMs(d.TotalDurationMs), d.RunID)
	}
	b.WriteString("\n")
	if d.Error != "" {
		fmt.Fprintf(&b, "\n> **Failed:** %s\n", d.Error)
	}

	if len(d.Inputs) > 0 {
		b.WriteString("\n## Inputs\n")
		for _, k := range sortedKeysAny(d.Inputs) {
			fmt.Fprintf(&b, "- **%s:** %s\n", k, stringifyValue(d.Inputs[k]))
		}
	}

	if len(d.Steps) > 0 {
		b.WriteString("\n## Steps\n")
		for i, s := range d.Steps {
			meta := statusWord(s.Status)
			if !d.Client {
				meta = fmt.Sprintf("%s · %s · %s", statusWord(s.Status), durMs(s.DurationMs), costOrDash(s.CostUSD))
			}
			fmt.Fprintf(&b, "\n### %d. %s — %s\n", i+1, s.ID, meta)
			if s.Output != "" {
				fmt.Fprintf(&b, "\n```\n%s\n```\n", s.Output)
			}
		}
	}

	if d.FinalOutput != "" {
		b.WriteString("\n## Final output\n\n")
		b.WriteString(d.FinalOutput)
		b.WriteString("\n")
	}
	return b.String()
}

func buildReportHTML(d reportData) string {
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\"><head><meta charset=\"utf-8\">")
	fmt.Fprintf(&b, "<title>%s — report</title>", esc(d.RoutineName))
	b.WriteString(`<style>
body{font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;max-width:820px;margin:2rem auto;padding:0 1rem;color:#1a1a1a}
h1{margin-bottom:.25rem}
.meta{color:#666;font-size:.9rem;margin-bottom:1.5rem}
.err{background:#fdecea;border-left:3px solid #d33;padding:.5rem .75rem;margin:1rem 0}
.step{border:1px solid #eee;border-radius:8px;padding:.75rem 1rem;margin:.75rem 0}
.step h3{margin:0 0 .5rem}
.badge{font-size:.8rem;color:#666}
pre{background:#f6f8fa;border-radius:6px;padding:.75rem;overflow:auto;white-space:pre-wrap;word-break:break-word}
dl{display:grid;grid-template-columns:auto 1fr;gap:.25rem .75rem}
dt{font-weight:600}
</style></head><body>`)
	fmt.Fprintf(&b, "<h1>%s</h1>", esc(d.RoutineName))

	b.WriteString(`<div class="meta">Status: ` + esc(statusWord(d.Status)))
	if !d.Client {
		fmt.Fprintf(&b, " · Cost: %s · Duration: %s · Run: %s",
			esc(costOrDash(d.TotalCostUSD)), esc(durMs(d.TotalDurationMs)), esc(d.RunID))
	}
	b.WriteString("</div>")
	if d.Error != "" {
		fmt.Fprintf(&b, `<div class="err"><strong>Failed:</strong> %s</div>`, esc(d.Error))
	}

	if len(d.Inputs) > 0 {
		b.WriteString("<h2>Inputs</h2><dl>")
		for _, k := range sortedKeysAny(d.Inputs) {
			fmt.Fprintf(&b, "<dt>%s</dt><dd>%s</dd>", esc(k), esc(stringifyValue(d.Inputs[k])))
		}
		b.WriteString("</dl>")
	}

	if len(d.Steps) > 0 {
		b.WriteString("<h2>Steps</h2>")
		for i, s := range d.Steps {
			b.WriteString(`<div class="step">`)
			badge := statusWord(s.Status)
			if !d.Client {
				badge = fmt.Sprintf("%s · %s · %s", statusWord(s.Status), durMs(s.DurationMs), costOrDash(s.CostUSD))
			}
			fmt.Fprintf(&b, `<h3>%d. %s <span class="badge">%s</span></h3>`, i+1, esc(s.ID), esc(badge))
			if s.Output != "" {
				fmt.Fprintf(&b, "<pre>%s</pre>", esc(s.Output))
			}
			b.WriteString("</div>")
		}
	}

	if d.FinalOutput != "" {
		fmt.Fprintf(&b, "<h2>Final output</h2><pre>%s</pre>", esc(d.FinalOutput))
	}
	b.WriteString("</body></html>")
	return b.String()
}

// statusWord renders a run/step status in plain words for a non-engineer.
func statusWord(s string) string {
	switch strings.ToLower(s) {
	case "completed", "succeeded", "ok":
		return "Succeeded"
	case "failed", "error":
		return "Failed"
	case "cancelled", "canceled":
		return "Cancelled"
	case "running":
		return "Running"
	case "":
		return "—"
	default:
		return strings.ToUpper(s[:1]) + s[1:]
	}
}

func costOrDash(c float64) string {
	if c <= 0 {
		return "—"
	}
	return fmt.Sprintf("$%.4f", c)
}

func durMs(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return formatElapsed(time.Duration(ms) * time.Millisecond)
}

func sortedKeysAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stringifyValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

func init() {
	routineReportCmd.Flags().StringVarP(&reportOutFile, "out", "o", "", "write the report to a file instead of stdout")
	routineReportCmd.Flags().BoolVar(&reportClient, "client", false, "redacted client-facing view: no run-id / cost / per-step metadata")
	pipelineCmd.AddCommand(routineReportCmd)
}

// gatherReport assembles reportData from the run detail + its journal timeline.
func gatherReport(client *cli.Client, ctx context.Context, runID string, clientMode bool) (reportData, error) {
	detail, err := client.GetPipelineRun(ctx, runID)
	if err != nil {
		return reportData{}, err
	}
	name := detail.PipelineName
	if name == "" {
		name = detail.PipelineSlug
	}
	d := reportData{
		RoutineName:     name,
		RunID:           detail.ID,
		Status:          detail.Status,
		Inputs:          detail.Inputs,
		FinalOutput:     detail.Output,
		TotalCostUSD:    detail.CostUSD,
		TotalDurationMs: detail.DurationMs,
		Error:           detail.ErrorMessage,
		Client:          clientMode,
	}
	rows := fetchRunEvents(client, detail.PipelineSlug)
	d.Steps = reportStepsFromEvents(rows, runID, stringifyOutputs(detail.StepOutputs))
	return d, nil
}

// stringifyOutputs coerces the loosely-typed step_outputs map (string values
// usually, but a structured value gets JSON-serialized) to strings.
func stringifyOutputs(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = stringifyValue(v)
	}
	return out
}

// fetchRunEvents pulls a routine's recent journal timeline (the same
// include_steps stream watch/logs use). Best-effort: nil on any failure so the
// report degrades to a step_outputs-only step list.
func fetchRunEvents(client *cli.Client, slug string) []watchEntry {
	if slug == "" {
		return nil
	}
	resp, err := client.Get("/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) +
		"/pipelines/" + url.PathEscape(slug) + "/runs?include_steps=1&limit=200")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var rows []watchEntry
	if json.NewDecoder(resp.Body).Decode(&rows) != nil {
		return nil
	}
	return rows
}

// reportStepsFromEvents derives an ordered step list from the run's journal
// timeline (step.started/completed/failed), attaching each step's output
// preview from the run's step_outputs. Best-effort: no events yields step rows
// built from step_outputs alone (unordered but present).
func reportStepsFromEvents(rows []watchEntry, runID string, stepOutputs map[string]string) []reportStep {
	if len(rows) == 0 {
		return stepsFromOutputsOnly(stepOutputs)
	}
	// Oldest-first so first-seen order is execution order.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	order := []string{}
	seen := map[string]bool{}
	byStep := map[string]*reportStep{}
	for _, r := range rows {
		if r.RunID != runID {
			continue
		}
		sid := payloadStepID(r.Payload)
		if sid == "" {
			continue
		}
		if !seen[sid] {
			seen[sid] = true
			order = append(order, sid)
			byStep[sid] = &reportStep{ID: sid, Status: "running"}
		}
		switch r.EntryType {
		case "pipeline.step.completed":
			byStep[sid].Status = "completed"
			byStep[sid].CostUSD = payloadFloat(r.Payload, "cost_usd")
			byStep[sid].DurationMs = int64(payloadFloat(r.Payload, "duration_ms"))
		case "pipeline.step.failed":
			byStep[sid].Status = "failed"
		}
	}
	out := make([]reportStep, 0, len(order))
	for _, sid := range order {
		s := byStep[sid]
		if o, ok := stepOutputs[sid]; ok {
			s.Output = truncatePreview(o, 2000)
		}
		out = append(out, *s)
	}
	if len(out) == 0 {
		return stepsFromOutputsOnly(stepOutputs)
	}
	return out
}

func stepsFromOutputsOnly(stepOutputs map[string]string) []reportStep {
	out := make([]reportStep, 0, len(stepOutputs))
	keys := make([]string, 0, len(stepOutputs))
	for k := range stepOutputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, reportStep{ID: k, Status: "completed", Output: truncatePreview(stepOutputs[k], 2000)})
	}
	return out
}

func truncatePreview(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}
