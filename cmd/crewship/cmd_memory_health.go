package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// memoryHealthCmd is a subcommand of the existing `memory` CLI that
// surfaces the 5-metric health score from /api/v1/memory/health.
// Kept in its own file so the local-filesystem subcommands in
// cmd_memory.go don't grow another set of imports.
var memoryHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Print the 5-metric memory health score (via server API)",
	Long: `Fetch the current memory health snapshot for the caller's workspace
(or a single crew with --crew). The five metrics — freshness, coverage,
coherence, efficiency, reachability — combine into a weighted overall
score in [0, 100] using the Auto-Dream weights (25/25/20/15/15).

Unlike the other 'memory' subcommands which read local filesystem
indexes, 'memory health' hits the running server and requires a login
token (see 'crewship login').`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		path := "/api/v1/memory/health"
		if crew, _ := cmd.Flags().GetString("crew"); crew != "" {
			crewID, err := resolveCrewID(client, crew)
			if err != nil {
				return err
			}
			path += "?crew_id=" + crewID
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			WorkspaceID string             `json:"workspace_id"`
			CrewID      string             `json:"crew_id"`
			Overall     float64            `json:"overall"`
			Metrics     map[string]float64 `json:"metrics"`
			Details     map[string]any     `json:"details"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(body)
		}
		if f.Format == "yaml" {
			return f.YAML(body)
		}

		// Coloured overall: red <50, yellow 50–75, green ≥75. The band
		// boundaries are arbitrary but match the Auto-Dream reference
		// implementation so operators used to that tool map cleanly.
		color := cli.Red
		switch {
		case body.Overall >= 75:
			color = cli.Green
		case body.Overall >= 50:
			color = cli.Yellow
		}
		scope := "workspace-wide"
		if body.CrewID != "" {
			scope = "crew " + body.CrewID
		}
		fmt.Printf("Memory health (%s): %s%.0f/100%s\n\n", scope, color, body.Overall, cli.Reset)
		for _, k := range []string{"freshness", "coverage", "coherence", "efficiency", "reachability"} {
			v, ok := body.Metrics[k]
			if !ok {
				continue
			}
			fmt.Printf("  %-14s %6.1f\n", k, v)
		}
		return nil
	},
}

func init() {
	memoryHealthCmd.Flags().String("crew", "", "Scope to a single crew by slug or ID")
	memoryCmd.AddCommand(memoryHealthCmd)
}
