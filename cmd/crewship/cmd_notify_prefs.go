package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// notifyPrefsCmd groups the caller's OWN server-side notification-category
// preferences (issue #1412) — the Linear/Novu-style category x channel
// matrix. Nested under the pre-existing `notify` command (local desktop-
// notification toggle, cmd_notify.go) since that's the namespace the
// issue's CLI surface names; distinct from `notifychannel`, which manages
// the delivery TARGETS (admin-scoped workspace channels + self-service
// personal channels) this matrix routes to.
var notifyPrefsCmd = &cobra.Command{
	Use:   "prefs",
	Short: "Get or set your server-side notification preference matrix (approvals, escalations, …)",
	Long: `View and edit YOUR OWN category x channel notification preference matrix.

A cell is 'off' (default — never delivered) or 'immediate' (delivered as
soon as the event happens; digest batching windows are a v2 feature, not
built here). Categories: approvals, escalations, runs.failed,
runs.completed, chat.replies, security, budget, system, memory.

The special category "*" mutes a channel entirely, overriding every other
cell for that channel.

Examples:
  crewship notify prefs get
  crewship notify prefs set --category approvals --channel nch_abc123 --state immediate
  crewship notify prefs set --category "*" --channel nch_abc123 --state immediate   # mute that channel`,
}

// notifyPrefCellRow mirrors notifyroute.PrefCell for CLI JSON I/O without
// importing the server-side package into the CLI binary.
type notifyPrefCellRow struct {
	Category  string `json:"category"`
	ChannelID string `json:"channel_id"`
	State     string `json:"state"`
}

var notifyPrefsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show your notification preference matrix",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/me/notification-prefs")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Cells []notifyPrefCellRow `json:"cells"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"CATEGORY", "CHANNEL", "STATE"}
		rows := make([][]string, 0, len(body.Cells))
		for _, c := range body.Cells {
			rows = append(rows, []string{c.Category, truncateString(c.ChannelID, 24), c.State})
		}
		return f.Auto(body.Cells, headers, rows)
	},
}

var notifyPrefsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set one cell in your notification preference matrix",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		category, _ := cmd.Flags().GetString("category")
		channelID, _ := cmd.Flags().GetString("channel")
		state, _ := cmd.Flags().GetString("state")
		if category == "" {
			return fmt.Errorf("--category is required")
		}
		if channelID == "" {
			return fmt.Errorf("--channel is required")
		}
		switch state {
		case "off", "immediate":
		default:
			return fmt.Errorf("--state must be 'off' or 'immediate'")
		}

		client := newAPIClient()
		resp, err := client.Put("/api/v1/me/notification-prefs", map[string]any{
			"cells": []notifyPrefCellRow{{Category: category, ChannelID: channelID, State: state}},
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Preference set: category=%s channel=%s state=%s", category, channelID, state))
		return nil
	},
}

func init() {
	notifyPrefsSetCmd.Flags().String("category", "", `Category (approvals, escalations, runs.failed, runs.completed, chat.replies, security, budget, system, memory, or "*" to mute the channel)`)
	notifyPrefsSetCmd.Flags().String("channel", "", "Channel id (see 'crewship notifychannel list')")
	notifyPrefsSetCmd.Flags().String("state", "", "off | immediate")

	notifyPrefsCmd.AddCommand(notifyPrefsGetCmd)
	notifyPrefsCmd.AddCommand(notifyPrefsSetCmd)
	notifyCmd.AddCommand(notifyPrefsCmd) // notifyCmd itself is declared+registered in cmd_notify.go
}
