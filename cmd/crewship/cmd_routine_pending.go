package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Deferred-dispatch CLI (v122): list + cancel parked triggers (delay /
// ttl / debounce / priority). Mirrors the /pipelines/pending endpoints.

var routinePendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "Inspect or cancel accepted deferred routine starts",
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
		status, _ := cmd.Flags().GetString("status")
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/pending?status=%s", ws, url.QueryEscape(status)))
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
			fmt.Fprintln(w, "PENDING ID\tROUTINE\tSTATUS\tATTEMPTS\tPRIORITY\tDEBOUNCE KEY\tFIRES AT\tRECIPE VERSION\tRUN ID\tREASON")
			for _, r := range rows {
				version := "live at dispatch (legacy/deferred)"
				if r.PinnedVersion != nil {
					version = fmt.Sprintf("v%d", *r.PinnedVersion)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.PipelineSlug, r.Status, r.DispatchAttempts, r.Priority, r.DebounceKey, r.FireAt, version, r.RunID, r.LastError)
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
	// Inputs is the server's read-only preview of the preset (#2489):
	// safe primitives kept, credential/file/secret values replaced by a
	// {type: ...} marker. It cannot replay the run.
	Inputs           map[string]any `json:"inputs" yaml:"inputs"`
	PinnedVersion    *int           `json:"pinned_version" yaml:"pinned_version"`
	ID               string         `json:"id" yaml:"id"`
	PipelineSlug     string         `json:"pipeline_slug" yaml:"pipeline_slug"`
	DebounceKey      string         `json:"debounce_key" yaml:"debounce_key"`
	Priority         int            `json:"priority" yaml:"priority"`
	FireAt           string         `json:"fire_at" yaml:"fire_at"`
	ExpiresAt        *string        `json:"expires_at" yaml:"expires_at"`
	NextAttemptAt    *string        `json:"next_attempt_at" yaml:"next_attempt_at"`
	Status           string         `json:"status" yaml:"status"`
	RunID            string         `json:"run_id" yaml:"run_id"`
	DispatchAttempts int            `json:"dispatch_attempts" yaml:"dispatch_attempts"`
	LastError        string         `json:"last_error" yaml:"last_error"`
	CanCancel        bool           `json:"can_cancel" yaml:"can_cancel"`
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

var routinePendingGetCmd = &cobra.Command{
	Use:   "get <pending_id>",
	Short: "Inspect an accepted deferred start, including dispatch failures",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-pending/%s", client.GetWorkspaceID(), url.PathEscape(args[0])))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row pendingTriggerRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(row, func() {
			fmt.Printf("Deferred start: %s\nRoutine: %s\nStatus: %s\nDispatch attempts: %d\nScheduled for: %s\n", row.ID, row.PipelineSlug, row.Status, row.DispatchAttempts, row.FireAt)
			if row.ExpiresAt != nil {
				fmt.Printf("Expires at: %s\n", *row.ExpiresAt)
			}
			if row.NextAttemptAt != nil {
				fmt.Printf("Next attempt: %s\n", *row.NextAttemptAt)
			}
			if row.RunID != "" {
				fmt.Printf("Run: %s\n", row.RunID)
			}
			if row.LastError != "" {
				fmt.Printf("Reason: %s\n", row.LastError)
			}
		})
	},
}

func init() {
	routinePendingListCmd.Flags().String("status", "pending", "Filter receipts: pending, fired, failed, expired, cancelled, all")
	routinePendingCmd.AddCommand(routinePendingGetCmd)
	routinePendingCmd.AddCommand(routinePendingListCmd)
	routinePendingCmd.AddCommand(routinePendingCancelCmd)
	pipelineCmd.AddCommand(routinePendingCmd)
}
