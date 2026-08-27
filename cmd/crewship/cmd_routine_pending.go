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

// Deferred-dispatch CLI (v122): list + cancel parked triggers (delay /
// ttl / debounce / priority). Mirrors the /pipelines/pending endpoints.

var routinePendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List or cancel deferred (delayed/debounced) routine triggers",
}

var routinePendingListCmd = &cobra.Command{
	Use:   "list",
	Short: "List not-yet-fired deferred triggers in this workspace",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/pending", ws))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []pendingTriggerRow
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// "Nothing pending" is the normal state, so it is also the state a
		// polling script hits on nearly every run — it must be an empty list,
		// not the sentence this used to print under `-f json`.
		var flush tabFlush
		if err := resolvedFormatter(cmd).AutoHuman(rows, func() {
			if len(rows) == 0 {
				fmt.Println("No deferred triggers pending.")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PENDING ID\tROUTINE\tPRIORITY\tDEBOUNCE KEY\tFIRES AT")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", r.ID, r.PipelineSlug, r.Priority, r.DebounceKey, r.FireAt)
			}
			flush.of(w)
		}); err != nil {
			return err
		}
		return flush.err
	},
}

// pendingTriggerRow is one deferred (delayed/debounced) routine trigger.
type pendingTriggerRow struct {
	ID           string `json:"id" yaml:"id"`
	PipelineSlug string `json:"pipeline_slug" yaml:"pipeline_slug"`
	DebounceKey  string `json:"debounce_key" yaml:"debounce_key"`
	Priority     int    `json:"priority" yaml:"priority"`
	FireAt       string `json:"fire_at" yaml:"fire_at"`
}

var routinePendingCancelCmd = &cobra.Command{
	Use:   "cancel <pending_id>",
	Short: "Cancel a not-yet-fired deferred trigger",
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
		resp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/pending/%s/cancel", ws, args[0]),
			http.NoBody)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Cancelled pending trigger %s.\n", args[0])
		return nil
	},
}

func init() {
	routinePendingCmd.AddCommand(routinePendingListCmd)
	routinePendingCmd.AddCommand(routinePendingCancelCmd)
	pipelineCmd.AddCommand(routinePendingCmd)
}
