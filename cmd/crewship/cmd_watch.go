package main

import (
	"fmt"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// watchCmd is a top-level shortcut for `crewship journal --follow`.
// Same SSE-backed live tail, but the name is what users actually reach
// for when they want to "watch what's happening." Filter flags mirror
// the journal subcommand so muscle memory transfers.
var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Live-tail the Crew Journal (alias of `journal --follow`)",
	Long: `Watch the Crew Journal as events stream in. SSE-based; reconnects
on transient failure and resumes via Last-Event-ID. Press Ctrl-C to exit.

Examples:
  crewship watch
  crewship watch --severity warn,error
  crewship watch --crew backend-team
  crewship watch --type peer.escalation,keeper.decision`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		crewFlag, _ := cmd.Flags().GetString("crew")
		agentID, _ := cmd.Flags().GetString("agent")
		typeFilter, _ := cmd.Flags().GetString("type")
		severityFilter, _ := cmd.Flags().GetString("severity")

		q := url.Values{}
		if crewFlag != "" {
			crewID, err := resolveCrewID(client, crewFlag)
			if err != nil {
				return err
			}
			q.Set("crew_id", crewID)
		}
		if agentID != "" {
			q.Set("agent_id", agentID)
		}
		if typeFilter != "" {
			q.Set("entry_type", typeFilter)
		}
		if severityFilter != "" {
			q.Set("severity", severityFilter)
		}

		fmt.Fprintf(os.Stderr, "%swatching journal — Ctrl-C to exit%s\n", cli.Dim, cli.Reset)
		return followJournal(client, q)
	},
}

func init() {
	watchCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	watchCmd.Flags().String("agent", "", "Filter by agent ID")
	watchCmd.Flags().String("type", "", "Comma-separated entry types")
	watchCmd.Flags().String("severity", "", "Comma-separated severities (info,notice,warn,error)")
}
