package main

import (
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

func newRoutineFixtureTestCmd() *cobra.Command {
	var stepID, input, outputs, outputFile, env, metadata, secrets string
	cmd := &cobra.Command{Use: "fixture-test <recipe.json|recipe.yaml>", Short: "Test one step offline using explicit fixtures, without external actions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		canonical, err := pipeline.ToCanonicalJSON(raw)
		if err != nil {
			return err
		}
		inputs, err := parseInputFixture(input)
		if err != nil {
			return err
		}
		upstream, err := parseOutputsFixture(outputs)
		if err != nil {
			return err
		}
		in := pipeline.FixtureStepInput{Definition: canonical, StepID: stepID, Inputs: inputs, StepOutputs: upstream}
		in.Env, err = parseOutputsFixture(env)
		if err != nil {
			return fmt.Errorf("env samples: %w", err)
		}
		in.Metadata, err = parseInputFixture(metadata)
		if err != nil {
			return fmt.Errorf("metadata samples: %w", err)
		}
		in.Secrets, err = parseOutputsFixture(secrets)
		if err != nil {
			return fmt.Errorf("secret samples: %w", err)
		}
		if outputFile != "" {
			raw, err := os.ReadFile(outputFile)
			if err != nil {
				return err
			}
			value := string(raw)
			in.FixtureOutput = &value
		}
		result, err := pipeline.TestStepWithFixtures(cmd.Context(), in)
		if err != nil {
			return err
		}
		f := resolvedFormatter(cmd)
		switch f.Format {
		case "yaml":
			err = f.YAML(result)
		case "ndjson":
			err = f.NDJSON(result)
		default:
			err = f.JSON(result)
		}
		if err != nil {
			return err
		}
		if !result.Valid {
			return fmt.Errorf("fixture output failed validation: %s", result.ValidationReason)
		}
		return nil
	}}
	cmd.Flags().StringVar(&stepID, "step", "", "top-level step ID to test")
	cmd.Flags().StringVar(&input, "input", "", "input values: JSON object inline or @file.json")
	cmd.Flags().StringVar(&outputs, "outputs", "", "captured upstream outputs: JSON object inline or @file.json")
	cmd.Flags().StringVar(&outputFile, "fixture-output-file", "", "file containing the replacement output for an agent/http/script step")
	cmd.Flags().StringVar(&env, "env", "", "sample env/run values: string-valued JSON object inline or @file.json")
	cmd.Flags().StringVar(&metadata, "metadata", "", "sample run metadata: JSON object inline or @file.json")
	cmd.Flags().StringVar(&secrets, "secrets", "", "fake secret samples: string-valued JSON object inline or @file.json; never real credentials")
	_ = cmd.MarkFlagRequired("step")
	return cmd
}

func init() { pipelineCmd.AddCommand(newRoutineFixtureTestCmd()) }
