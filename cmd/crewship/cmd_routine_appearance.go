package main

// A routine's icon and colour.
//
// Presentation, not behaviour — which is exactly why it is its own
// endpoint and its own command rather than a field of `routine save`.
// Save rewrites the definition, recomputes definition_hash, can mint a
// new version and re-runs the governance risk classifier. None of that
// should happen because somebody picked a different colour, so this
// writes two columns and leaves the definition alone.

import (
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// appearanceRow is the subset of the pipeline response this command
// reads back. The endpoint returns the whole routine; only these two
// fields are the point.
type appearanceRow struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Icon  string `json:"icon,omitempty"`
	Color string `json:"color,omitempty"`
}

var routineAppearanceCmd = &cobra.Command{
	Use:   "appearance",
	Short: "A routine's icon and colour — how it renders in lists",
	Long: `A workspace ends up with dozens of routines that look identical in a
list. An icon and a colour make one findable at a glance; they are the
same icon set crews and projects use.

This is presentation only. It never touches the definition, so setting
an icon does not create a routine version, does not re-run the
governance risk check, and does not invalidate a save token.

Leave a routine unset and the UI derives a stable icon from its slug, so
every routine still looks different without anyone choosing.

Examples:
  crewship routine appearance get monthly-accounting-pack
  crewship routine appearance set monthly-accounting-pack --icon receipt --color amber
  crewship routine appearance set monthly-accounting-pack --clear
`,
}

var routineAppearanceGetCmd = &cobra.Command{
	Use:   "get <slug>",
	Short: "Show a routine's icon and colour",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s", ws, args[0]))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row appearanceRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(row, func() {
			if row.Icon == "" && row.Color == "" {
				fmt.Printf("%s: no icon set — the UI derives one from the slug.\n", row.Slug)
				fmt.Printf("  Set one: crewship routine appearance set %s --icon <name> --color <palette>\n", row.Slug)
				return
			}
			fmt.Printf("%s\n", row.Slug)
			fmt.Printf("  icon:  %s\n", orDash(row.Icon))
			fmt.Printf("  color: %s\n", orDash(row.Color))
		})
	},
}

var routineAppearanceSetCmd = &cobra.Command{
	Use:   "set <slug>",
	Short: "Set (or clear) a routine's icon and colour",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		clear, _ := cmd.Flags().GetBool("clear")
		iconSet := cmd.Flags().Changed("icon")
		colorSet := cmd.Flags().Changed("color")
		if clear && (iconSet || colorSet) {
			return fmt.Errorf("--clear cannot be combined with --icon or --color")
		}
		if !clear && !iconSet && !colorSet {
			return fmt.Errorf("give --icon, --color, or --clear")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		// Only the flags actually given are sent. The endpoint treats an
		// absent field as "leave it alone" and an empty string as
		// "clear it", so setting a colour must not silently wipe an icon
		// the caller never mentioned.
		body := map[string]string{}
		if clear {
			body["icon"] = ""
			body["color"] = ""
		}
		if iconSet {
			icon, _ := cmd.Flags().GetString("icon")
			body["icon"] = icon
		}
		if colorSet {
			color, _ := cmd.Flags().GetString("color")
			body["color"] = color
		}

		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Patch(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/appearance", ws, args[0]),
			body,
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row appearanceRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(row, func() {
			if row.Icon == "" && row.Color == "" {
				fmt.Printf("Cleared %s — back to the icon derived from its slug.\n", args[0])
				return
			}
			fmt.Printf("Updated %s: icon %s, color %s\n", args[0], orDash(row.Icon), orDash(row.Color))
		})
	},
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func init() {
	routineAppearanceSetCmd.Flags().String("icon", "", "icon name (same set as crews and projects, e.g. receipt, rocket, database)")
	routineAppearanceSetCmd.Flags().String("color", "", "palette id: blue, emerald, violet, amber, rose, cyan, lime, fuchsia")
	routineAppearanceSetCmd.Flags().Bool("clear", false, "remove both, falling back to the slug-derived icon")

	routineAppearanceCmd.AddCommand(routineAppearanceGetCmd)
	routineAppearanceCmd.AddCommand(routineAppearanceSetCmd)
	pipelineCmd.AddCommand(routineAppearanceCmd)
}
