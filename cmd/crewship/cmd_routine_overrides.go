package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Per-step prompt/model override CLI (v121). Lets an operator tweak one
// step's prompt or tier without bumping the routine version. Mirrors the
// /pipelines/{slug}/steps/{stepId}/override endpoints.

var routineStepOverrideCmd = &cobra.Command{
	Use:   "step-override",
	Short: "Override a step's prompt/model without bumping the routine version",
}

var routineStepOverrideSetCmd = &cobra.Command{
	Use:   "set <slug> <step_id>",
	Short: "Set a prompt and/or model override for one step",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		prompt, _ := cmd.Flags().GetString("prompt")
		model, _ := cmd.Flags().GetString("model")
		if prompt == "" && model == "" {
			return fmt.Errorf("pass --prompt and/or --model")
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Do("PUT",
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/steps/%s/override", ws, args[0], args[1]),
			map[string]any{"prompt": prompt, "model_override": model})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Override set for step %q of %q (applies on next run, no version bump).\n", args[1], args[0])
		return nil
	},
}

var routineStepOverrideClearCmd = &cobra.Command{
	Use:   "clear <slug> <step_id>",
	Short: "Remove a step's override (revert to authored value)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Do("DELETE",
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/steps/%s/override", ws, args[0], args[1]),
			http.NoBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Override cleared for step %q of %q.\n", args[1], args[0])
		return nil
	},
}

var routineStepOverrideListCmd = &cobra.Command{
	Use:   "list <slug>",
	Short: "List active step overrides for a routine",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/overrides", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Overrides []struct {
				StepID        string `json:"step_id"`
				Prompt        string `json:"prompt"`
				ModelOverride string `json:"model_override"`
			} `json:"overrides"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(body.Overrides) == 0 {
			fmt.Println("No step overrides — routine runs as authored.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "STEP\tMODEL\tPROMPT")
		for _, o := range body.Overrides {
			p := o.Prompt
			if len(p) > 50 {
				p = p[:50] + "…"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", o.StepID, o.ModelOverride, p)
		}
		return w.Flush()
	},
}

func init() {
	routineStepOverrideSetCmd.Flags().String("prompt", "", "replacement prompt for the step")
	routineStepOverrideSetCmd.Flags().String("model", "", "model/tier override for the step")
	routineStepOverrideCmd.AddCommand(routineStepOverrideSetCmd)
	routineStepOverrideCmd.AddCommand(routineStepOverrideClearCmd)
	routineStepOverrideCmd.AddCommand(routineStepOverrideListCmd)
	pipelineCmd.AddCommand(routineStepOverrideCmd)
}
