package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// consolidateCmd triggers the memory-consolidation worker — the
// background job that compacts agent/crew memory into summaries. Live
// against POST /api/v1/consolidate/run. When no summarizer is
// configured the backend returns 202 Accepted with a note; this command
// surfaces that to the operator rather than pretending success.
var consolidateCmd = &cobra.Command{
	Use:   "consolidate",
	Short: "Force a memory consolidation run",
	Long: `Trigger the memory-consolidation worker — the background process that
compacts agent/crew long-running memory into summaries. Normally runs on
a schedule; this command forces an immediate run.

Examples:
  crewship consolidate run
  crewship consolidate run --crew cmo2pe4dj0005ba0a129f
  crewship consolidate run --since 24h
  crewship consolidate run --since 7d

Note: --crew accepts a crew slug or ID (slugs are resolved automatically).
      --since accepts Go durations (24h, 90m) plus d/w shorthand.`,
}

var consolidateRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Force an immediate consolidation run",
	// `run` takes no positional args — scope is passed via --crew/--since.
	// A bare `crewship consolidate run backend-team` would currently be
	// silently ignored, which is a footgun when users expect slug args.
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		crew, _ := cmd.Flags().GetString("crew")
		since, _ := cmd.Flags().GetString("since")
		body := map[string]string{}
		if crew != "" {
			// Resolve slug→ID the same way every other crew-scoped command
			// does (issue #616). A CUID passes through untouched; a slug is
			// looked up against /api/v1/crews so `--crew backend-team` works.
			crewID, err := resolveCrewID(client, crew)
			if err != nil {
				return err
			}
			body["crew_id"] = crewID
		}
		if since != "" {
			body["since"] = since
		}

		resp, err := client.Post("/api/v1/consolidate/run", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			Triggered bool   `json:"triggered"`
			Accepted  bool   `json:"accepted"`
			WorkerID  string `json:"worker_id"`
			Note      string `json:"note"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(out)
		}
		if f.Format == "yaml" {
			return f.YAML(out)
		}

		switch {
		case out.Triggered:
			fmt.Printf("Consolidation triggered (worker_id=%s)\n", out.WorkerID)
		case out.Accepted:
			fmt.Printf("Accepted, but skipped: %s\n", out.Note)
		default:
			fmt.Println("Consolidation request submitted.")
		}
		return nil
	},
}

func init() {
	consolidateRunCmd.Flags().String("crew", "", "Limit consolidation to a single crew (slug or ID)")
	consolidateRunCmd.Flags().String("since", "", "Only consider journal entries newer than this window (e.g. 24h, 90m, 7d, 2w)")

	consolidateCmd.AddCommand(consolidateRunCmd)
}
