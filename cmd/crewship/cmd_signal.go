package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// signalCmd is the topic-scoped half of signal delivery: address the EVENT,
// not a run. `crewship routine signal <run_id>` stays the way to wake one run
// you already have the id for; this is the way to wake everything parked on
// an event when you do not — which is the normal position for anything
// emitting an event rather than answering a specific run.

var signalCmd = &cobra.Command{
	Use:   "signal",
	Short: "Deliver workspace-wide signals to routines parked on a wait:event step",
	Long: `A routine step of kind wait:event parks durably until a named event
arrives. Delivering by TOPIC wakes every run in the workspace waiting on that
event_type at once — the producer does not need to know which runs are parked.

To wake one specific run instead, use crewship routine signal <run_id>.

Examples:
  crewship signal send --event-type mission.status_change --payload '{"status":"done"}'
  crewship signal send --event-type deploy.finished`,
}

var signalSendCmd = &cobra.Command{
	Use:   "send",
	Short: "Deliver an event to every run in the workspace waiting on it",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		eventType, _ := cmd.Flags().GetString("event-type")
		if eventType == "" {
			return fmt.Errorf("--event-type <type> is required")
		}
		payload, _ := cmd.Flags().GetString("payload")

		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/signals", ws),
			map[string]any{"event_type": eventType, "payload": payload})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Delivered int      `json:"delivered"`
			RunIDs    []string `json:"run_ids"`
			Truncated bool     `json:"truncated"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// Nothing waiting is a normal outcome, not a failure — say so
		// plainly rather than printing an empty success that reads like
		// the signal went somewhere.
		if body.Delivered == 0 {
			fmt.Printf("Signal %q delivered to 0 runs (nothing is waiting on that event).\n", eventType)
			return nil
		}
		fmt.Printf("Signal %q delivered to %d run(s):\n", eventType, body.Delivered)
		for _, id := range body.RunIDs {
			fmt.Printf("  %s\n", id)
		}
		if body.Truncated {
			cli.PrintWarning("More runs are still waiting than one call can deliver to — run the command again.")
		}
		return nil
	},
}

func init() {
	signalSendCmd.Flags().String("event-type", "", "event_type the wait:event steps are waiting on (REQUIRED)")
	signalSendCmd.Flags().String("payload", "", "string payload that becomes each wait step's output")
	signalCmd.AddCommand(signalSendCmd)
	rootCmd.AddCommand(signalCmd)
}
