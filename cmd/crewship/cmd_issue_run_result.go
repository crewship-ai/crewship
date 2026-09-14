package main

// `crewship issue result` — the CLI half of
// GET /api/v1/crews/{crewId}/issues/{identifier}/runs/{runId}/result.
//
// `issue runs` lists an issue's runs with their status; the full result of
// one of them — what the worker actually reported back — was reachable only
// from the web issue panel. An agent reading its own crew's work through the
// CLI could see that a run finished and not what it produced.

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var issueResultCmd = &cobra.Command{
	Use:   "result <identifier> <run-id>",
	Short: "Show what one of an issue's runs reported back",
	Long: `Print a single run's result summary, outcome and status.

The run must belong to this issue — one of its own assignments, or one of its
tasks' — otherwise the server answers 404 rather than reading across issues.

The id is the assignment id — the 'id' field of 'crewship issue runs
<identifier> -f json'. It is NOT the RUN column of the table that command
prints: that column is the journal trace id, and passing it answers 404.`,
	Example: `  crewship issue runs ENG-4 -f json
  crewship issue result ENG-4 m_1a0864071c85b082f6adb2c
  crewship issue result ENG-4 m_1a0864071c85b082f6adb2c -f json`,
	Args: cobra.ExactArgs(2),
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
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/runs/%s/result",
			issue.CrewID, url.PathEscape(derefStr(issue.Identifier, issue.ID)), url.PathEscape(args[1])))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			// The table printed by `issue runs` shows the journal trace id in
			// its RUN column, while this endpoint matches on the assignment id.
			// Anyone reading the id off that table lands here on their first
			// try, so say where the right one is rather than just refusing.
			if resp.StatusCode == http.StatusNotFound {
				return fmt.Errorf("%w\n\nThe id here is the assignment id, not the RUN column of 'crewship issue runs'.\nTake the 'id' field from: crewship issue runs %s -f json", err, args[0])
			}
			return err
		}
		var out struct {
			ResultSummary string `json:"result_summary" yaml:"result_summary"`
			Outcome       string `json:"outcome"        yaml:"outcome"`
			Status        string `json:"status"         yaml:"status"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Printf("Run:      %s\nStatus:   %s\n", args[1], out.Status)
			if out.Outcome != "" {
				fmt.Printf("Outcome:  %s\n", out.Outcome)
			}
			if out.ResultSummary == "" {
				// A run that reported nothing is a real answer, not an empty
				// screen: say so rather than printing a blank line.
				fmt.Println("\nThe run reported no result summary.")
				return
			}
			fmt.Printf("\n%s\n", out.ResultSummary)
		})
	},
}

func init() {
	issueCmd.AddCommand(issueResultCmd)
}
