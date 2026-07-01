package main

import (
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// The `nuke` command group is the single, discoverable home for every workspace
// teardown. Each subcommand maps to exactly one server capability, so there's
// one CLI per endpoint (API↔CLI parity, no duplicate surfaces):
//
//	crewship nuke all          full teardown (data + inbox + escalations + runtimes)
//	crewship nuke data         DB entities only (no inbox/escalations/runtimes)
//	crewship nuke inbox        DELETE /api/v1/inbox            [--kind]
//	crewship nuke escalations  DELETE crew escalations         [--crew]
//	crewship nuke runtimes     POST  /api/v1/admin/prune-crew-runtimes
//
// `crewship seed --nuke` is an alias for `nuke all` (it calls the same nukeAll
// orchestrator), kept for the seed-then-reseed workflow.
//
// The two full-DB wipes (all, data) reuse the confirmNuke typed-slug gate. The
// narrower purges (inbox, escalations, runtimes) use a lighter --yes gate and
// refuse to run non-interactively without it.
var nukeCmd = &cobra.Command{
	Use:   "nuke",
	Short: "Tear down workspace contents (data, inbox, escalations, docker runtimes)",
	Long: `Destructive teardown of the active workspace.

  crewship nuke all          # everything: DB entities + inbox + escalations + docker runtimes
  crewship nuke data         # DB entities only (issues, crews, pipelines, …)
  crewship nuke inbox        # inbox items      (--kind to scope)
  crewship nuke escalations  # crew escalations (--crew to scope; default all crews)
  crewship nuke runtimes     # each crew's docker container(s)+volumes (cached images kept)

'crewship seed --nuke' is an alias for 'nuke all'.`,
}

// nukeGate is the lighter confirmation for the narrow purges (inbox,
// escalations, runtimes): require --yes, and never proceed non-interactively
// without it. The full-DB wipes (all, data) use confirmNuke's typed-slug gate
// instead — a heavier bar for a heavier blast radius.
func nukeGate(cmd *cobra.Command, what string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	if yes {
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "This permanently deletes %s in the active workspace. Re-run with --yes to confirm.\n", what)
	return fmt.Errorf("aborted: --yes required")
}

var nukeAllCmd = &cobra.Command{
	Use:   "all",
	Short: "Full teardown: DB entities + inbox + escalations + docker runtimes",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		if err := confirmNuke(cmd, client, cli.EffectiveServer(flagServer, flagProfile, cliCfg)); err != nil {
			return err
		}
		return nukeAll(cmd.Context(), client)
	},
}

var nukeDataCmd = &cobra.Command{
	Use:   "data",
	Short: "Delete DB entities only (issues/projects/labels/agents/credentials/integrations/pipelines/crews)",
	Long: `Delete every workspace-scoped DB entity, but NOT inbox items, escalations,
or docker runtimes. This is the entity half of a full 'nuke all'.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		if err := confirmNuke(cmd, client, cli.EffectiveServer(flagServer, flagProfile, cliCfg)); err != nil {
			return err
		}
		failures, err := nukeData(cmd.Context(), client)
		if err != nil {
			return err
		}
		if len(failures) > 0 {
			return fmt.Errorf("workspace data cleanup had %d failures: %s", len(failures), strings.Join(failures, "; "))
		}
		cli.PrintSuccess("Workspace data cleaned")
		return nil
	},
}

var nukeInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Delete inbox items for the workspace (--kind to scope)",
	Long: `Hard-delete inbox items. Scope to one kind to clear, e.g., the failed-run
spam a broken scheduled routine piles up without touching pending waitpoints:

  crewship nuke inbox --kind failed_run --yes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		kind, _ := cmd.Flags().GetString("kind")
		if kind != "" {
			switch kind {
			case "waitpoint", "escalation", "failed_run", "message":
			default:
				return fmt.Errorf("invalid --kind %q (want waitpoint|escalation|failed_run|message)", kind)
			}
		}
		scope := "ALL inbox items"
		if kind != "" {
			scope = fmt.Sprintf("all '%s' inbox items", kind)
		}
		if err := nukeGate(cmd, scope); err != nil {
			return err
		}
		if err := nukeInbox(cmd.Context(), newAPIClient(), kind); err != nil {
			return err
		}
		cli.PrintSuccess("Inbox purged")
		return nil
	},
}

var nukeEscalationsCmd = &cobra.Command{
	Use:   "escalations",
	Short: "Delete escalations (--crew to scope; default all crews in workspace)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		crew, _ := cmd.Flags().GetString("crew")
		what := "ALL escalations for every crew"
		if crew != "" {
			what = fmt.Sprintf("all escalations for crew %q", crew)
		}
		if err := nukeGate(cmd, what); err != nil {
			return err
		}
		if err := nukeEscalations(cmd.Context(), newAPIClient(), crew); err != nil {
			return err
		}
		cli.PrintSuccess("Escalations purged")
		return nil
	},
}

var nukeRuntimesCmd = &cobra.Command{
	Use:   "runtimes",
	Short: "Remove every crew's docker container(s)+volumes (cached images kept)",
	Long: `Tear down the live id-scoped docker containers and volumes (agent home,
crew shared, sidecar) of every crew in the active workspace. Cached devcontainer
images (crewship-cache:<hash>) are NOT removed, so a reseed reuses them instead
of rebuilding. A docker-less server reports nothing to do.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := nukeGate(cmd, "every crew's docker container(s)+volumes (agent home, crew shared)"); err != nil {
			return err
		}
		if err := nukeRuntimes(cmd.Context(), newAPIClient()); err != nil {
			return err
		}
		cli.PrintSuccess("Crew docker runtimes torn down")
		return nil
	},
}

func init() {
	nukeCmd.PersistentFlags().Bool("yes", false, "Confirm the deletion (skips the prompt)")
	nukeInboxCmd.Flags().String("kind", "", "Scope to one kind: waitpoint|escalation|failed_run|message")
	nukeEscalationsCmd.Flags().String("crew", "", "Crew slug or ID (default: all crews in the workspace)")

	nukeCmd.AddCommand(nukeAllCmd)
	nukeCmd.AddCommand(nukeDataCmd)
	nukeCmd.AddCommand(nukeInboxCmd)
	nukeCmd.AddCommand(nukeEscalationsCmd)
	nukeCmd.AddCommand(nukeRuntimesCmd)
}
