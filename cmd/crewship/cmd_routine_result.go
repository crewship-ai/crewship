package main

// Routine result subcommand. Answers "show me what run X produced" — the
// one thing every other per-run view deliberately omits. `routine logs`
// prints status/cost/error, `records` prints the run list, `runs` is an
// event log; none surface the run's final `output` (the deliverable),
// which until now was only shown transiently inline at trigger time. The
// data is already on the wire (GET /pipeline-runs/{id} returns `output`) —
// this command re-fetches it any time after the run.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var routineResultCmd = &cobra.Command{
	Use:   "result <run_id>",
	Short: "Print the final deliverable (output) of a past routine run",
	Long: `Re-fetches a finished run and prints its final output — the deliverable a
client actually cares about, which 'routine logs' omits.

  crewship routine result run_abc123
  crewship routine result run_abc123 --format json | jq -r .output

Structured (JSON) output is pretty-printed. Pair with 'routine logs
<run> --show-outputs' for the per-step breakdown.`,
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
		detail, err := client.GetPipelineRun(cmd.Context(), runID)
		if err != nil {
			return err
		}

		f := resolvedFormatter(cmd)
		switch f.Format {
		case "json":
			return f.JSON(detail)
		case "yaml":
			return f.YAML(detail)
		case "ndjson":
			return f.NDJSON(detail)
		}

		// Human view: one status line, the error (if any), then the
		// deliverable.
		fmt.Printf("Run %s: %s", detail.ID, strings.ToUpper(detail.Status))
		if detail.DurationMs > 0 || detail.CostUSD > 0 {
			fmt.Printf(" (%dms, $%.4f)", detail.DurationMs, detail.CostUSD)
		}
		fmt.Println()
		if detail.ErrorMessage != "" {
			fmt.Printf("Error: %s\n", detail.ErrorMessage)
			if detail.FailedAtStep != "" {
				fmt.Printf("Failed at step: %s\n", detail.FailedAtStep)
			}
		}
		if detail.Output != "" {
			fmt.Println("\nFinal output:")
			fmt.Println(indent(prettyOutput(detail.Output), "  "))
			return nil
		}
		// No output — distinguish "run produced nothing" from "not done yet"
		// so the absence isn't read as a silent bug.
		if detail.IsTerminal() {
			fmt.Println("\n(no final output recorded for this run)")
		} else {
			fmt.Printf("\n(run is %s — no final output yet)\n", strings.ToLower(detail.Status))
		}
		return nil
	},
}

// prettyOutput indents a JSON object/array for readability and returns any
// other string unchanged (the common agent-free-text case). Kept lenient:
// anything that doesn't parse as JSON prints verbatim.
func prettyOutput(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return s
	}
	var v interface{}
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	return string(b)
}

func init() {
	routineResultCmd.Flags().Bool("json", false, "Deprecated alias for --format json")
	pipelineCmd.AddCommand(routineResultCmd)
}
