package main

import (
	"crypto/rand"
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var issueWorkCmd = &cobra.Command{
	Use: "work <identifier>", Short: "Take over, hand off, or submit an issue with a durable receipt", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		action, _ := cmd.Flags().GetString("action")
		target, _ := cmd.Flags().GetString("target")
		note, _ := cmd.Flags().GetString("note")
		operation, _ := cmd.Flags().GetString("operation-id")
		if operation == "" {
			operation = rand.Text()
		}
		revision := issue.WorkRevision
		if cmd.Flags().Changed("revision") {
			revision, _ = cmd.Flags().GetInt("revision")
		}
		res, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/work", issue.CrewID, url.PathEscape(derefStr(issue.Identifier, issue.ID))), map[string]any{"action": action, "target_id": target, "note": note, "operation_id": operation, "revision": revision})
		if err != nil {
			return err
		}
		if err := cli.CheckError(res); err != nil {
			return err
		}
		var receipt map[string]any
		if err := cli.ReadJSON(res, &receipt); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(receipt, func() { cli.PrintSuccess("Work updated for " + args[0]) })
	},
}

func init() {
	issueWorkCmd.Flags().String("action", "take_over", "take_over | handoff_agent | handoff_human | submit")
	issueWorkCmd.Flags().String("target", "", "Target user or agent ID (for handoff)")
	issueWorkCmd.Flags().String("note", "", "Result, verification, or instructions for the next worker")
	issueWorkCmd.Flags().String("operation-id", "", "Stable ID when retrying the same operation")
	issueWorkCmd.Flags().Int("revision", 0, "Expected work revision (defaults to current issue revision)")
	issueCmd.AddCommand(issueWorkCmd)
}
