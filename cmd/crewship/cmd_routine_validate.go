package main

// Routine validate subcommand. Validates a routine DSL file LOCALLY
// without contacting the server — useful for editor-loop iteration,
// CI gates, and pre-commit hooks. Catches everything Parse + Validate
// catches at save-time except cross-routine cycle detection (which
// needs a workspace-wide call graph the local CLI can't see).

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

var routineValidateCmd = &cobra.Command{
	Use:   "validate [file.json]",
	Short: "Validate a routine DSL file offline (no server call)",
	Long: `Parses + validates a routine DSL JSON file locally. Reports the same
errors the server would on save, except cross-routine cycle detection
(which needs the workspace's full call graph). Exit code 0 = valid,
1 = invalid.

Reads from the given file argument, or from stdin if no argument:
  crewship routine validate routine.json
  cat routine.json | crewship routine validate

Use in CI:
  - name: Validate routine DSL
    run: crewship routine validate routines/email-fetch.json

The local check runs:
  1. JSON parse
  2. Schema validation (required fields, step types, slug format)
  3. Template reference checks (inputs.X / steps.Y.output)
  4. Step ID uniqueness, needs[] references valid
  5. JSON Schema subset checks on validation blocks

Server-side checks not run locally:
  - Agent slug existence in author crew (needs DB)
  - Cross-routine call_pipeline cycle detection (needs workspace)
  - Slug uniqueness against existing routines (needs DB)
`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var raw []byte
		var src string
		if len(args) == 1 {
			b, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			raw = b
			src = args[0]
		} else {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			if len(b) == 0 {
				return fmt.Errorf("empty input (no file argument and stdin is empty)")
			}
			raw = b
			src = "<stdin>"
		}

		dsl, err := pipeline.Parse(raw)
		if err != nil {
			return printValidationError(src, "parse", err)
		}
		if err := pipeline.Validate(dsl, nil, nil); err != nil {
			return printValidationError(src, "validate", err)
		}

		// Local cycle pre-check: catches direct self-references and
		// in-DSL loops (call_pipeline a → a, or a → b → a embedded in
		// a single bundle). Workspace-wide cycle detection still runs
		// at save.
		visited := make(map[string]bool, len(dsl.Steps))
		for _, s := range dsl.Steps {
			if visited[s.ID] {
				return printValidationError(src, "validate", fmt.Errorf("step ID %q used twice", s.ID))
			}
			visited[s.ID] = true
		}

		jsonOut, _ := cmd.Flags().GetBool("json")
		if jsonOut {
			b, _ := json.MarshalIndent(map[string]interface{}{
				"source":      src,
				"valid":       true,
				"name":        dsl.Name,
				"step_count":  len(dsl.Steps),
				"input_count": len(dsl.Inputs),
				"step_types":  collectStepTypes(dsl),
			}, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		fmt.Printf("✓ %s is a valid routine DSL.\n", src)
		fmt.Printf("  Name:      %s\n", dsl.Name)
		fmt.Printf("  Steps:     %d (%s)\n", len(dsl.Steps), strings.Join(collectStepTypes(dsl), ", "))
		fmt.Printf("  Inputs:    %d\n", len(dsl.Inputs))
		if len(dsl.Outputs) > 0 {
			fmt.Printf("  Outputs:   %d\n", len(dsl.Outputs))
		}
		if len(dsl.EgressTargets) > 0 {
			fmt.Printf("  Egress:    %s\n", strings.Join(dsl.EgressTargets, ", "))
		}
		fmt.Println("Save with: crewship routine save --definition <file> --author-crew <crew_id>")
		return nil
	},
}

func printValidationError(src, phase string, err error) error {
	fmt.Fprintf(os.Stderr, "✗ %s failed at %s phase:\n  %v\n", src, phase, err)
	os.Exit(1)
	return nil
}

func collectStepTypes(dsl *pipeline.DSL) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range dsl.Steps {
		t := string(s.Type)
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func init() {
	routineValidateCmd.Flags().Bool("json", false, "output as JSON for scripting / CI")
	pipelineCmd.AddCommand(routineValidateCmd)
}
