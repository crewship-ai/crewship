package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Long: `Override one step's prompt and/or model without bumping the routine
version. The override applies from the next run; clearing it reverts the
step to its authored prompt and model. Setting or clearing requires MANAGER
or above; listing is open to every workspace member.

Examples:
  crewship routine step-override set pr-review extract --model smart
  crewship routine step-override set pr-review extract --prompt "Extract entities as JSON"
  crewship routine step-override list pr-review -f json
  crewship routine step-override clear pr-review extract`,
}

// stepOverrideWriteResult mirrors the {"ok":true,"step_id":...} answer of
// the PUT and DELETE override endpoints, with the routine slug added so a
// script reading -f json output knows which routine it refers to.
type stepOverrideWriteResult struct {
	OK     bool   `json:"ok" yaml:"ok"`
	Slug   string `json:"slug" yaml:"slug"`
	StepID string `json:"step_id" yaml:"step_id"`
}

// A 2xx with no body (204 from an older or proxied server) still means the
// write landed, so it reports success for the requested step rather than
// failing a change that already applied.
func decodeStepOverrideWrite(resp *http.Response, slug, stepID string) (stepOverrideWriteResult, error) {
	out := stepOverrideWriteResult{OK: true, Slug: slug, StepID: stepID}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		return out, fmt.Errorf("decode response: %w", err)
	}
	out.Slug = slug
	return out, nil
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
		out, err := decodeStepOverrideWrite(resp, args[0], args[1])
		if err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Printf("Override set for step %q of %q (applies on next run, no version bump).\n", args[1], args[0])
		})
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
		out, err := decodeStepOverrideWrite(resp, args[0], args[1])
		if err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Printf("Override cleared for step %q of %q.\n", args[1], args[0])
		})
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
			Overrides []stepOverrideRow `json:"overrides" yaml:"overrides"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// "Runs as authored" is the state of nearly every routine, so the
		// empty case is the overwhelmingly common one — and it answered a
		// sentence under `-f json`.
		//
		// The machine rows carry the whole replacement prompt; the human
		// column cuts it at 50 characters, which is fine to read and useless
		// to diff.
		if body.Overrides == nil {
			body.Overrides = []stepOverrideRow{}
		}
		var flush tabFlush
		if err := resolvedFormatter(cmd).AutoHuman(body.Overrides, func() {
			if len(body.Overrides) == 0 {
				fmt.Println("No step overrides — routine runs as authored.")
				return
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
			flush.of(w)
		}); err != nil {
			return err
		}
		return flush.err
	},
}

// stepOverrideRow is one active per-step override on a routine.
type stepOverrideRow struct {
	StepID        string `json:"step_id" yaml:"step_id"`
	Prompt        string `json:"prompt" yaml:"prompt"`
	ModelOverride string `json:"model_override" yaml:"model_override"`
}

func init() {
	routineStepOverrideSetCmd.Flags().String("prompt", "", "replacement prompt for the step")
	routineStepOverrideSetCmd.Flags().String("model", "", "model/tier override for the step")
	routineStepOverrideCmd.AddCommand(routineStepOverrideSetCmd)
	routineStepOverrideCmd.AddCommand(routineStepOverrideClearCmd)
	routineStepOverrideCmd.AddCommand(routineStepOverrideListCmd)
	pipelineCmd.AddCommand(routineStepOverrideCmd)
}
