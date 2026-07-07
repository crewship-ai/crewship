package main

// Routine step-run subcommand. The "unit test for one step": execute a
// single agent_run step against a supplied input fixture — no DAG, no
// upstream steps, no persisted run record — and print its output +
// validation verdict + cost. Closes the recurring-cost half of the debug
// loop that dry-run (no execution) and a full run (~8 min, real tokens)
// leave open. See POST /pipelines/{slug}/step_run.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	stepRunInput        string
	stepRunTierOverride string
)

var routineStepRunCmd = &cobra.Command{
	Use:   "step-run <slug> <step>",
	Short: "Execute one agent_run step against a fixture, without the full pipeline",
	Long: `Run a SINGLE agent_run step of a routine against a given input fixture, in
isolation — no upstream steps, no DAG, no persisted run record — and print
its output, validation verdict, and cost.

The "unit test for a step": iterate on one parse/extract prompt in seconds
instead of running the whole pipeline (dry-run doesn't execute; a full run is
too slow and costs real tokens).

  crewship routine step-run parse-invoice extract --input @sample.json
  crewship routine step-run parse-invoice extract --input '{"name":"a.pdf"}' --tier-override fast
  crewship routine step-run parse-invoice extract --input @s.json -f json | jq -r .output

--input is a JSON object (the step's inputs fixture), inline or @file.json.
--tier-override swaps the step's tier (trivial|fast|moderate|smart) for cheap
structural iteration; a step-level model_override still wins.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, stepID := args[0], args[1]
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		inputs, err := parseInputFixture(stepRunInput)
		if err != nil {
			return err
		}

		client := newAPIClient()
		res, err := client.StepRunRoutine(cmd.Context(), slug, stepID, inputs, stepRunTierOverride)
		if err != nil {
			return err
		}

		f := resolvedFormatter(cmd)
		switch f.Format {
		case "json":
			return f.JSON(res)
		case "yaml":
			return f.YAML(res)
		case "ndjson":
			return f.NDJSON(res)
		}

		// Human view: verdict + resolved model line, cost line, then the
		// full deliverable (like `routine result` / `logs --show-outputs`).
		verdict := "PASS"
		if !res.Valid {
			verdict = "FAIL"
		}
		fmt.Printf("Step %s (%s) → %s  [%s %s]\n", res.StepID, res.StepType, verdict, res.Adapter, res.Model)
		fmt.Printf("  cost $%.4f · %d→%d tok · %dms · simulated (no run record)\n",
			res.CostUSD, res.TokensIn, res.TokensOut, res.DurationMs)
		if !res.Valid && res.ValidationReason != "" {
			fmt.Printf("  validation: %s\n", res.ValidationReason)
		}
		fmt.Println("\nOutput:")
		fmt.Println(indent(prettyOutput(res.Output), "  "))
		return nil
	},
}

// parseInputFixture reads the --input fixture: an inline JSON object, or
// @file.json. Empty is allowed (a step with no {{ inputs.* }} refs). The
// fixture must be a JSON object — the shape `{{ inputs.X }}` resolves against.
func parseInputFixture(spec string) (map[string]any, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	raw := spec
	if strings.HasPrefix(spec, "@") {
		b, err := os.ReadFile(spec[1:])
		if err != nil {
			return nil, fmt.Errorf("read fixture %q: %w", spec[1:], err)
		}
		raw = string(b)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("--input must be a JSON object (inline or @file.json): %w", err)
	}
	return m, nil
}

func init() {
	routineStepRunCmd.Flags().StringVar(&stepRunInput, "input", "", "input fixture: JSON object inline or @file.json")
	routineStepRunCmd.Flags().StringVar(&stepRunTierOverride, "tier-override", "", "override the step tier (trivial|fast|moderate|smart)")
	pipelineCmd.AddCommand(routineStepRunCmd)
}
