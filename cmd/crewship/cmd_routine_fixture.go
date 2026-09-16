package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

// The default is in-process — the command's documented promise is "without
// contacting Crewship", and like `validate` it must work with no token and
// no server. --remote sends the identical FixtureStepInput to
// POST /api/v1/workspaces/{ws}/pipelines/fixture_test instead, which is what
// an agent in a container (a token, no local Go build) needs, and what proves
// the server's build reaches the same verdict as this binary's.
func newRoutineFixtureTestCmd() *cobra.Command {
	var stepID, input, outputs, outputFile, env, metadata, secrets string
	var remote bool
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
		var result *pipeline.FixtureStepResult
		if remote {
			result, err = remoteFixtureTest(in)
		} else {
			result, err = pipeline.TestStepWithFixtures(cmd.Context(), in)
		}
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
	cmd.Flags().BoolVar(&remote, "remote", false, "run the fixture test on the configured server (needs login + workspace) instead of in this binary; same input, same report")
	_ = cmd.MarkFlagRequired("step")
	return cmd
}

// remoteFixtureTest is the --remote path: the same FixtureStepInput the
// in-process call takes, sent as the endpoint's request body. The server
// answers 400 for a recipe or step it cannot test (the in-process error),
// and 403 below the create tier — both surface as the CLI's usual API error.
func remoteFixtureTest(in pipeline.FixtureStepInput) (*pipeline.FixtureStepResult, error) {
	if err := requireAuth(); err != nil {
		return nil, err
	}
	if err := requireWorkspace(); err != nil {
		return nil, err
	}
	client := newAPIClient()
	resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/fixture_test", client.GetWorkspaceID()), in)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var result pipeline.FixtureStepResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode fixture test response: %w", err)
	}
	return &result, nil
}

func init() { pipelineCmd.AddCommand(newRoutineFixtureTestCmd()) }
