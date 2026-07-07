package main

// Routine capabilities subcommand (#862, PRD #848 Pillar 4). One discovery
// dump — routine DSL schema + the crew's container capabilities + connected
// integrations with their enabled tool names + agent slugs + usable runtimes —
// so an LLM (or a human) can author a `routine validate`-clean DSL one-shot
// instead of piecing it together from ~8 commands.
//
//	crewship routine capabilities <crew> -f json | claude -p "write a routine that…"

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var routineCapabilitiesCmd = &cobra.Command{
	Use:   "capabilities <crew>",
	Short: "Dump everything needed to author a routine for a crew (schema + resources + integrations + agents + runtimes)",
	Long: `Return, in ONE bundle, everything an author (human or LLM) needs to write a
valid routine for a crew: the DSL JSON schema, the crew's container
capabilities (datastores + installed CLIs), connected integrations WITH their
enabled tool names, agent slugs, and the runtimes actually wired in this build.

  crewship routine capabilities acct                 # readable summary
  crewship routine capabilities acct -f json         # full machine-readable bundle (incl. schema)
  crewship routine capabilities acct -f json | claude -p "write a routine that reconciles invoices"

The JSON form is the one-shot authoring input; the default human view is a
scannable summary (the schema is omitted from it — use -f json for that).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		caps, err := client.CrewCapabilities(cmd.Context(), crewID)
		if err != nil {
			return err
		}

		f := resolvedFormatter(cmd)
		switch f.Format {
		case "json":
			return f.JSON(caps)
		case "yaml":
			return f.YAML(caps)
		case "ndjson":
			return f.NDJSON(caps)
		}

		// Human summary — scannable, schema omitted (it's ~pages of JSON).
		fmt.Printf("Capabilities for crew %s\n", caps.CrewSlug)

		fmt.Printf("\nAgents (%d) — agent_run steps reference these slugs:\n", len(caps.Agents))
		for _, a := range caps.Agents {
			fmt.Printf("  - %s\n", a.Slug)
		}

		if len(caps.Container.Tools) > 0 {
			names := make([]string, 0, len(caps.Container.Tools))
			for _, t := range caps.Container.Tools {
				names = append(names, t.Name)
			}
			fmt.Printf("\nContainer CLIs: %s\n", strings.Join(names, ", "))
		}
		if len(caps.Container.Datastores) > 0 {
			fmt.Println("\nDatastores (declare in resources.datastores):")
			for _, d := range caps.Container.Datastores {
				if d.Port != "" {
					fmt.Printf("  - %s (%s) — host %s port %s\n", d.Name, d.Type, d.Host, d.Port)
				} else {
					fmt.Printf("  - %s (%s) — host %s\n", d.Name, d.Type, d.Host)
				}
			}
		}

		fmt.Printf("\nIntegrations (%d):\n", len(caps.Integrations))
		for _, ig := range caps.Integrations {
			label := ig.DisplayName
			if label == "" {
				label = ig.Name
			}
			if len(ig.Tools) > 0 {
				fmt.Printf("  - %s: %s\n", label, strings.Join(ig.Tools, ", "))
			} else {
				fmt.Printf("  - %s (no tools enabled yet — toggle them in the dashboard)\n", label)
			}
		}

		fmt.Printf("\nRuntimes:\n")
		fmt.Printf("  type: code    → %s (wired)", strings.Join(caps.Runtimes.Code.Wired, ", "))
		if len(caps.Runtimes.Code.ReservedUnwired) > 0 {
			fmt.Printf("   [reserved, do NOT use: %s]", strings.Join(caps.Runtimes.Code.ReservedUnwired, ", "))
		}
		fmt.Println()
		if len(caps.Runtimes.ScriptInterpreters) > 0 {
			exts := make([]string, 0, len(caps.Runtimes.ScriptInterpreters))
			for ext := range caps.Runtimes.ScriptInterpreters {
				exts = append(exts, ext)
			}
			sort.Strings(exts)
			pairs := make([]string, 0, len(exts))
			for _, ext := range exts {
				pairs = append(pairs, fmt.Sprintf("%s→%s", ext, caps.Runtimes.ScriptInterpreters[ext]))
			}
			fmt.Printf("  type: script  → %s\n", strings.Join(pairs, "  "))
		}

		fmt.Printf("\n(Run with -f json for the full bundle including the DSL schema.)\n")
		return nil
	},
}

func init() {
	pipelineCmd.AddCommand(routineCapabilitiesCmd)
}
