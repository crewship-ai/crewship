package main

// Routine schema subcommand. Prints the published routine JSON Schema
// (draft 2020-12) that the CLI validates against and that IDEs use for
// autocomplete. Emitting it locally lets an author (or an AI authoring a
// routine one-shot) read the exact machine-readable contract without
// guessing from docs — the schema is the source of truth for step kinds,
// required fields, and enums.

import (
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/schemas"
	"github.com/spf13/cobra"
)

var routineSchemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Print the routine DSL JSON Schema (authoring contract)",
	Long: `Prints the JSON Schema (draft 2020-12) a routine definition is validated
against. This is the same contract 'crewship routine validate' enforces
and IDEs consume for autocomplete.

  crewship routine schema                 # print to stdout
  crewship routine schema > routine.schema.json
  crewship routine schema -o routine.schema.json

Wire it into an editor for inline validation, or hand it to an agent so it
authors a valid routine in one shot.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out, _ := cmd.Flags().GetString("output")
		if out != "" {
			if err := os.WriteFile(out, schemas.RoutineV1, 0o644); err != nil {
				return fmt.Errorf("write schema to %s: %w", out, err)
			}
			fmt.Fprintf(os.Stderr, "wrote routine schema to %s\n", out)
			return nil
		}
		if _, err := os.Stdout.Write(schemas.RoutineV1); err != nil {
			return err
		}
		// Ensure a trailing newline for clean terminal output.
		if n := len(schemas.RoutineV1); n == 0 || schemas.RoutineV1[n-1] != '\n' {
			fmt.Println()
		}
		return nil
	},
}

func init() {
	routineSchemaCmd.Flags().StringP("output", "o", "", "Write the schema to a file instead of stdout")
	pipelineCmd.AddCommand(routineSchemaCmd)
}
