package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Observability CLI (trigger.dev-informed): replay a failed run with its
// original inputs, list failures grouped by fingerprint, and bulk-replay
// a group after shipping a fix. Mirrors the /pipelines/runs/* endpoints
// (Core rule #3 — every API endpoint gets a CLI command).

var routineReplayCmd = &cobra.Command{
	Use:   "replay <run_id>",
	Short: "Re-run a prior run with its original inputs (marked is_replay)",
	Long: `Loads the run's captured inputs and invokes the routine again. The
new run is stamped is_replay=true + replay_of=<run_id> so steps can
short-circuit side effects via {{ env.is_replay }}, and it inherits the
original run's tags so it groups with the source.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.WithTimeout(evalRunTimeout).Post(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/runs/%s/replay", ws, args[0]),
			map[string]any{},
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var res struct {
			RunID  string  `json:"run_id"`
			Status string  `json:"status"`
			Output string  `json:"output"`
			Cost   float64 `json:"cost_usd"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("Replayed %s → new run %s: %s ($%.4f)\n", args[0], res.RunID, res.Status, res.Cost)
		if res.Output != "" {
			fmt.Printf("Output: %s\n", res.Output)
		}
		return nil
	},
}

var routineErrorsCmd = &cobra.Command{
	Use:   "errors",
	Short: "List failed runs grouped by error fingerprint",
	Long: `Buckets the workspace's failed runs by a stable error fingerprint
(failing step + normalized message) so like failures group together.
Pick a fingerprint and replay the whole group with:

    crewship routine bulk-replay --fingerprint <fp>`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/runs/errors?limit=%d", ws, limit))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Groups []struct {
				Fingerprint  string   `json:"fingerprint"`
				Count        int      `json:"count"`
				PipelineSlug string   `json:"pipeline_slug"`
				FailedAtStep string   `json:"failed_at_step"`
				SampleError  string   `json:"sample_error"`
				RunIDs       []string `json:"run_ids"`
			} `json:"groups"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(body.Groups) == 0 {
			fmt.Println("No failed runs. 🎉")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "FINGERPRINT\tCOUNT\tROUTINE\tSTEP\tSAMPLE ERROR")
		for _, g := range body.Groups {
			msg := g.SampleError
			if len(msg) > 60 {
				msg = msg[:60] + "…"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", g.Fingerprint, g.Count, g.PipelineSlug, g.FailedAtStep, msg)
		}
		return w.Flush()
	},
}

var routineBulkReplayCmd = &cobra.Command{
	Use:   "bulk-replay",
	Short: "Replay all failed runs under a fingerprint (after a fix)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		fingerprint, _ := cmd.Flags().GetString("fingerprint")
		limit, _ := cmd.Flags().GetInt("limit")
		if fingerprint == "" {
			return fmt.Errorf("--fingerprint required (see `crewship routine errors`)")
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.WithTimeout(evalRunTimeout).Do(
			"POST",
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/runs/bulk_replay", ws),
			map[string]any{"fingerprint": fingerprint, "limit": limit},
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Requested int `json:"requested"`
			Replayed  int `json:"replayed"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("Bulk replay: %d/%d runs re-triggered for fingerprint %s\n", out.Replayed, out.Requested, fingerprint)
		return nil
	},
}

func init() {
	routineErrorsCmd.Flags().Int("limit", 50, "max fingerprint groups to list")
	routineBulkReplayCmd.Flags().String("fingerprint", "", "error fingerprint to replay (REQUIRED; from `routine errors`)")
	routineBulkReplayCmd.Flags().Int("limit", 50, "max runs to replay from the group")

	pipelineCmd.AddCommand(routineReplayCmd)
	pipelineCmd.AddCommand(routineErrorsCmd)
	pipelineCmd.AddCommand(routineBulkReplayCmd)
}
