package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// adminPruneLegacyCmd removes pre-C1 (slug-only) crew docker resources that
// survive a nuke+reseed and make every agent in the affected crew fail to start
// (surfacing to users as a generic "failed to start agent container").
//
// Unlike the rest of the `admin` group — which bypasses the API and writes the
// local SQLite DB directly — this command is HTTP-backed: the docker daemon
// lives behind the running server, not the local DB, so it needs an
// authenticated session and a reachable server.
var adminPruneLegacyCmd = &cobra.Command{
	Use:   "prune-legacy",
	Short: "Remove orphaned pre-C1 crew docker resources (admin; needs a running server)",
	Long: `Removes legacy slug-only docker volumes/containers (e.g.
"crewship-3-tools-engineering") left over from before the C1 naming change.

These survive "crewship seed --nuke" — which only clears the database — because
crew teardown removes the id-scoped names, never the orphaned slug-only ones.
While they exist, the runtime's legacy-resource guard blocks every agent in the
affected crew from starting, reported only as "failed to start agent container".

This talks to the server's docker daemon over the API and removes ONLY the
legacy names; the id-scoped resources the live runtime uses are never touched.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/admin/prune-legacy-resources", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Removed []string `json:"removed"`
			Count   int      `json:"count"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if out.Count == 0 {
			fmt.Println("No legacy C1 resources found — nothing to prune.")
			return nil
		}
		fmt.Printf("Pruned %d legacy resource(s):\n", out.Count)
		for _, name := range out.Removed {
			fmt.Printf("  - %s\n", name)
		}
		return nil
	},
}

func init() {
	adminCmd.AddCommand(adminPruneLegacyCmd)
}
