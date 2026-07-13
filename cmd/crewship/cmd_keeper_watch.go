package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/spf13/cobra"
)

// keeperWatchCmd groups the M1 watch-spec commands (issue #1001) around the
// same partial-update PUT /api/v1/admin/keeper/governance the other keeper
// subcommands use. The watch spec is the OWNER/ADMIN-authored policy the
// behavioral watchdog evaluates agent activity against: a set of structured
// presets plus free-form natural-language rules, injected into the Keeper
// evaluator prompts.
//
// Like the rest of the governance surface it is opt-in — authoring a watch
// spec does not enable the watchdog; `crewship keeper enable` does that.
var keeperWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Author the workspace watch spec (presets + free-form rules)",
	Long: `Configure the Keeper watchdog watch spec for the current workspace: the
policy agent activity is evaluated against. It has two parts:

  - presets: stable, curated rules toggled on/off
      (` + strings.Join(governance.PresetKeys(), ", ") + `)
  - free-form: natural-language rules you write, e.g.
      "flag any read of ~/.ssh or id_rsa; flag credential access outside 08:00-18:00"

Both are injected into the evaluator prompts as an authoritative policy. Empty =
the evaluator falls back to its built-in anti-pattern list. Authoring a spec does
not enable the watchdog — run 'crewship keeper enable' for that. OWNER/ADMIN only.

Examples:
  crewship keeper watch get
  crewship keeper watch set "flag any read of ~/.ssh or id_rsa"
  cat rules.txt | crewship keeper watch set -
  crewship keeper watch clear
  crewship keeper watch preset list
  crewship keeper watch preset add credentials
  crewship keeper watch preset remove egress`,
}

// enabledPresetSet builds a lookup set of the workspace's enabled preset keys.
func enabledPresetSet(gov keeperGovernance) map[string]bool {
	set := make(map[string]bool, len(gov.WatchPresets))
	for _, k := range gov.WatchPresets {
		set[k] = true
	}
	return set
}

// presetMark returns the ✓/space marker for a preset key. Shared by every
// preset listing so the enabled indicator can't drift between views.
func presetMark(enabled map[string]bool, key string) string {
	if enabled[key] {
		return cli.Green + "✓" + cli.Reset
	}
	return " "
}

// printKeeperWatch renders the watch-spec block shared by get + every mutation
// so the shape can't drift.
func printKeeperWatch(gov keeperGovernance) {
	fmt.Printf("%sWatch spec (workspace)%s\n", cli.Bold, cli.Reset)

	enabled := enabledPresetSet(gov)
	fmt.Printf("  Presets:\n")
	for _, k := range governance.PresetKeys() {
		fmt.Printf("    [%s] %s\n", presetMark(enabled, k), k)
	}

	if strings.TrimSpace(gov.WatchSpec) == "" {
		fmt.Printf("  Free-form rules: — (none)\n")
	} else {
		fmt.Printf("  Free-form rules:\n")
		for _, line := range strings.Split(gov.WatchSpec, "\n") {
			fmt.Printf("    %s\n", line)
		}
	}
}

var keeperWatchGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the current workspace watch spec",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		gov, err := getKeeperGovernance(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(gov, func() { printKeeperWatch(gov) })
	},
}

var keeperWatchSetCmd = &cobra.Command{
	Use:   "set <text|->",
	Short: "Set the free-form watch rules (use - to read from stdin)",
	Long: `Replace the free-form (natural-language) watch rules. Pass the rules as a
single argument, or "-" to read them from stdin (handy for multi-line rules).
Presets are unaffected — use 'keeper watch preset' for those.

Examples:
  crewship keeper watch set "flag any read of ~/.ssh or id_rsa"
  cat rules.txt | crewship keeper watch set -`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		spec := args[0]
		if spec == "-" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read watch spec from stdin: %w", err)
			}
			spec = strings.TrimRight(string(b), "\n")
		}
		// Mirror the server-side cap so the operator gets a specific error
		// before the round-trip (the server also enforces it).
		if len(spec) > governance.MaxWatchSpecLen {
			return fmt.Errorf("watch spec is %d bytes; the maximum is %d", len(spec), governance.MaxWatchSpecLen)
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client, map[string]any{"watch_spec": spec})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess("Watch rules updated for this workspace.")
			printKeeperWatch(out)
		})
	},
}

var keeperWatchClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the free-form watch rules (presets are unaffected)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		out, err := putKeeperGovernanceFields(client, map[string]any{"watch_spec": ""})
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess("Free-form watch rules cleared.")
			printKeeperWatch(out)
		})
	},
}

// keeperWatchPresetCmd groups the preset toggles. add/remove read-merge the
// preset array (the one place a read-merge is unavoidable — the wire field is
// the whole set, not a delta), then PUT the new array.
var keeperWatchPresetCmd = &cobra.Command{
	Use:   "preset",
	Short: "Toggle watch presets on or off",
}

var keeperWatchPresetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the preset catalog with a ✓ on the enabled ones",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		gov, err := getKeeperGovernance(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(gov, func() {
			enabled := enabledPresetSet(gov)
			fmt.Printf("%sWatch presets%s\n", cli.Bold, cli.Reset)
			for _, k := range governance.PresetKeys() {
				fmt.Printf("  [%s] %-12s %s\n", presetMark(enabled, k), k, governance.WatchPresets[k])
			}
		})
	},
}

// mutatePreset applies fn to the current preset set and PUTs the result. add
// and remove share it so the read-merge lives in one place.
func mutatePreset(key string, fn func(set map[string]bool)) error {
	if err := governance.ValidatePresets([]string{key}); err != nil {
		return err
	}
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return err
	}
	gov, err := getKeeperGovernance(client)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, k := range gov.WatchPresets {
		set[k] = true
	}
	fn(set)
	next := make([]string, 0, len(set))
	for k := range set {
		next = append(next, k)
	}
	sort.Strings(next)

	out, err := putKeeperGovernanceFields(client, map[string]any{"watch_presets": next})
	if err != nil {
		return err
	}
	return newFormatter().AutoHuman(out, func() {
		cli.PrintSuccess(fmt.Sprintf("Preset %q updated.", key))
		printKeeperWatch(out)
	})
}

var keeperWatchPresetAddCmd = &cobra.Command{
	Use:   "add <key>",
	Short: "Enable a watch preset",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutatePreset(args[0], func(set map[string]bool) { set[args[0]] = true })
	},
}

var keeperWatchPresetRemoveCmd = &cobra.Command{
	Use:   "remove <key>",
	Short: "Disable a watch preset",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutatePreset(args[0], func(set map[string]bool) { delete(set, args[0]) })
	},
}

func init() {
	keeperWatchPresetCmd.AddCommand(keeperWatchPresetListCmd)
	keeperWatchPresetCmd.AddCommand(keeperWatchPresetAddCmd)
	keeperWatchPresetCmd.AddCommand(keeperWatchPresetRemoveCmd)

	keeperWatchCmd.AddCommand(keeperWatchGetCmd)
	keeperWatchCmd.AddCommand(keeperWatchSetCmd)
	keeperWatchCmd.AddCommand(keeperWatchClearCmd)
	keeperWatchCmd.AddCommand(keeperWatchPresetCmd)
}
