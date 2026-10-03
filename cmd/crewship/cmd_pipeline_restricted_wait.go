package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Private receipts belong to the actor-scoped endpoint, including delayed jobs.
// Construct the path locally rather than following a server-supplied URL with
// the CLI's credentials.
func waitForRestrictedRoutineRun(cmd *cobra.Command, client *cli.Client, runID string, timeout time.Duration) error {
	if runID == "" {
		return fmt.Errorf("restricted routine receipt has no run_id")
	}
	ctx := cmd.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	endpoint := "/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) + "/restricted-routine-runs/" + url.PathEscape(runID)
	for {
		resp, err := client.WithContext(ctx).Get(endpoint)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			resp.Body.Close()
			return err
		}
		var result struct {
			RunID       string            `json:"run_id" yaml:"run_id"`
			Status      string            `json:"status" yaml:"status"`
			StepOutputs map[string]string `json:"step_outputs" yaml:"step_outputs"`
			CreatedAt   string            `json:"created_at" yaml:"created_at"`
		}
		err = cli.ReadJSON(resp, &result)
		if err != nil {
			return err
		}
		switch strings.ToLower(result.Status) {
		case "completed", "failed", "canceled", "cancelled", "needs_reconciliation":
			if err := resolvedFormatter(cmd).AutoHuman(result, func() {
				fmt.Printf("Run %s: %s\n", result.RunID, strings.ToUpper(result.Status))
				for step, output := range result.StepOutputs {
					fmt.Printf("  [%s]\n%s\n", step, indent(prettyOutput(output), "    "))
				}
			}); err != nil {
				return err
			}
			if !strings.EqualFold(result.Status, "completed") {
				return fmt.Errorf("routine run %s", result.Status)
			}
			return nil
		case "pending", "queued", "starting", "running", "scheduled", "in_progress", "deduped":
		default:
			return fmt.Errorf("unknown restricted routine status %q", result.Status)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for restricted run %s: %w", runID, ctx.Err())
		case <-timer.C:
		}
	}
}
