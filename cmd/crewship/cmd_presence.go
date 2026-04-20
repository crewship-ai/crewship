package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// presenceCmd is the Watch Roster surface — a live view of who is
// online/busy/blocked/offline across a crew or workspace. Live against
// GET /api/v1/presence/roster[?crew_id=…]; status flips are emitted by
// the agent runtime on transition, not by this CLI.
var presenceCmd = &cobra.Command{
	Use:   "presence",
	Short: "Watch Roster — who is online/busy/blocked",
	Long: `Show the Watch Roster — the live presence board tracking agent status
(online, busy, blocked, offline) across a crew or the full workspace.

Examples:
  crewship presence roster
  crewship presence roster --crew cmo2pe4dj0005ba0a129f

Note: --crew expects the crew ID today (slug→ID resolution is TBD).`,
}

var presenceRosterCmd = &cobra.Command{
	Use:   "roster",
	Short: "Show the current presence roster",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		crew, _ := cmd.Flags().GetString("crew")
		q := url.Values{}
		if crew != "" {
			q.Set("crew_id", crew)
		}
		path := "/api/v1/presence/roster"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Rows []struct {
				AgentID string         `json:"agent_id"`
				CrewID  string         `json:"crew_id"`
				Status  string         `json:"status"`
				Since   string         `json:"since"`
				Details map[string]any `json:"details"`
			} `json:"rows"`
			Count int `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body.Rows)
		}
		if f.Format == "yaml" {
			return f.YAML(body.Rows)
		}

		if len(body.Rows) == 0 {
			fmt.Println("(roster empty — no presence entries for scope)")
			return nil
		}
		header := []string{"AGENT", "CREW", "STATUS", "SINCE"}
		rows := make([][]string, 0, len(body.Rows))
		for _, r := range body.Rows {
			rows = append(rows, []string{r.AgentID, r.CrewID, r.Status, r.Since})
		}
		f.Table(header, rows)
		return nil
	},
}

func init() {
	presenceRosterCmd.Flags().String("crew", "", "Filter by crew ID (slug resolution not yet wired)")
	presenceCmd.AddCommand(presenceRosterCmd)
}
