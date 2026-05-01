package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// inspectCmd is the deterministic counterpart to `crewship explain`:
// fetches the same journal entries and renders them as a structured
// timeline (no LLM, fast). Use when you want to *see* what happened
// without paying for a summary call or waiting for tokens to stream.
//
// Sibling design:
//
//	crewship inspect <run-id>   — structured timeline (this command)
//	crewship explain <run-id>   — LLM-summarized narrative
//
// Both share fetchRun + the journal /api endpoint; explain feeds the
// entries to an agent, inspect formats them directly.
var inspectCmd = &cobra.Command{
	Use:   "inspect <run-id>",
	Short: "Show a structured timeline of a run (journal events, no LLM)",
	Long: `Print the journal entries for a run as a tabular timeline.

Each line shows: timestamp, severity chip, entry type, and summary. Costs
and tool calls are pulled out into a footer block when present.

Examples:
  crewship inspect r_abc
  crewship inspect r_abc --types error,exec.error,keeper.decision
  crewship inspect r_abc --format json | jq '.entries[]'
  crewship inspect r_abc --filter '.entries[] | select(.severity=="error")'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		runID := args[0]

		runMeta, err := fetchRun(client, runID)
		if err != nil {
			return err
		}
		if runMeta.AgentID == "" {
			return fmt.Errorf("run %s has no agent_id", runID)
		}

		windowStart, err := runWindowStart(client, runID)
		if err != nil {
			windowStart = time.Now().Add(-1 * time.Hour)
		}

		typesFilter, _ := cmd.Flags().GetString("types")
		// Fetch agent+window-scoped entries then filter to this run's
		// trace_id. Without the trace filter, a later run on the same agent
		// would fold its events into this timeline (and its cost/tool
		// totals), which is misleading.
		entries, err := fetchInspectEntries(client, runMeta.AgentID, windowStart, typesFilter)
		if err != nil {
			return err
		}
		entries = filterEntriesByTrace(entries, runID)

		// JSON / YAML / filter paths bypass the table formatter and emit the
		// raw entry list. Filter is jq-piped via emitJSONFiltered.
		f := newFormatter()
		jqExpr, _ := cmd.Flags().GetString("filter")
		if jqExpr != "" || f.Format == "json" || f.Format == "yaml" {
			payload := map[string]any{"run_id": runID, "agent_id": runMeta.AgentID, "entries": entries}
			if jqExpr != "" {
				return emitJSONFiltered(cmd, payload)
			}
			if f.Format == "yaml" {
				return f.YAML(payload)
			}
			return f.JSON(payload)
		}
		printInspectTable(runID, runMeta.AgentSlug, runMeta.AgentID, entries)
		return nil
	},
}

// fetchInspectEntries pulls journal entries for the agent since `from`,
// optionally filtered by entry types. Returns entries in chronological
// order (oldest first) so a timeline reads naturally.
func fetchInspectEntries(client *cli.Client, agentID string, from time.Time, types string) ([]map[string]any, error) {
	path := "/api/v1/journal" + queryString(
		"agent_id", agentID,
		"since", from.UTC().Format(time.RFC3339),
		"limit", "200",
		"entry_type", types,
	)
	var body struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := getJSON(client, path, &body); err != nil {
		return nil, err
	}
	// Server returns newest-first; reverse for timeline display.
	out := make([]map[string]any, len(body.Entries))
	for i, e := range body.Entries {
		out[len(body.Entries)-1-i] = e
	}
	return out, nil
}

// filterEntriesByTrace narrows entries to those whose trace_id matches
// the inspected run. journal entries record trace_id = run_id by
// convention (see internal/journal/types.go), so this is the canonical
// way to scope a multi-run agent's journal slice down to one run.
//
// If no entries have a trace_id (older entries pre-dating the trace
// rollout, or non-run-scoped events the user explicitly --types-included),
// they're kept on the assumption the user wants to see them.
func filterEntriesByTrace(entries []map[string]any, runID string) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		t, ok := e["trace_id"].(string)
		if !ok || t == "" {
			// No trace metadata — keep, since we can't disprove it belongs.
			out = append(out, e)
			continue
		}
		if t == runID {
			out = append(out, e)
		}
	}
	return out
}

// printInspectTable renders entries as a one-line-per-event timeline,
// followed by a roll-up of cost (if any cost.incurred entries exist) and
// tool call counts. Format mirrors `crewship journal` for muscle memory.
func printInspectTable(runID, agentSlug, agentID string, entries []map[string]any) {
	header := fmt.Sprintf("Run %s", runID)
	if agentSlug != "" {
		header += fmt.Sprintf(" · agent %s", agentSlug)
	} else if agentID != "" {
		header += fmt.Sprintf(" · agent %s", agentID)
	}
	fmt.Printf("%s%s%s  (%d entries)\n", cli.Bold, header, cli.Reset, len(entries))
	fmt.Println(strings.Repeat("─", 80))

	if len(entries) == 0 {
		fmt.Printf("%sno entries in journal window — run may be older than recall horizon%s\n",
			cli.Dim, cli.Reset)
		return
	}

	var totalCost float64
	toolCalls := 0
	errors := 0
	for _, e := range entries {
		ts, _ := e["ts"].(string)
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ts = t.Format("15:04:05")
		}
		entryType, _ := e["entry_type"].(string)
		severity, _ := e["severity"].(string)
		summary, _ := e["summary"].(string)

		color := cli.Gray
		switch severity {
		case "warn":
			color = cli.Yellow
		case "error":
			color = cli.Red
			errors++
		case "notice":
			color = cli.Cyan
		}
		if entryType == "tool_call" || strings.HasPrefix(entryType, "tool.") {
			toolCalls++
		}
		if entryType == "cost.incurred" {
			if payload, ok := e["payload"].(map[string]any); ok {
				if v, ok := payload["cost_usd"].(float64); ok {
					totalCost += v
				}
			}
		}

		fmt.Printf("%s%s%s  %s[%-7s]%s  %s%-22s%s  %s\n",
			cli.Dim, ts, cli.Reset,
			color, severity, cli.Reset,
			cli.Bold, truncateString(entryType, 22), cli.Reset,
			summary)
	}

	fmt.Println(strings.Repeat("─", 80))
	fmt.Printf("%stotal cost:%s $%.4f   %stool calls:%s %d   %serrors:%s %d\n",
		cli.Dim, cli.Reset, totalCost,
		cli.Dim, cli.Reset, toolCalls,
		cli.Dim, cli.Reset, errors)
}

func init() {
	inspectCmd.Flags().String("types", "", "Comma-separated entry types to include (default: all)")
	jqExprFlag(inspectCmd)
	addWatchFlag(inspectCmd)
	inspectCmd.RunE = watchWrap(inspectCmd.RunE)
}
