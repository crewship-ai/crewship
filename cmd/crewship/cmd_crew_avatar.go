package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// crewApplyAvatarStyleCmd wires POST /api/v1/crews/{crewId}/apply-avatar-style
// (internal/api/crew_config.go) — previously API-only, with no CLI parity
// (issue #966 part 3). Mirrors the crew toolbar's "Apply to all agents"
// avatar action: set every agent in the crew to the same avatar_style, or
// clear per-agent overrides back to their template default.
var crewApplyAvatarStyleCmd = &cobra.Command{
	Use:   "apply-avatar-style <slug-or-id>",
	Short: "Apply (or reset) an avatar style across every agent in a crew",
	Long: `Set the avatar_style override for every agent in a crew in one call, or
clear existing overrides so agents fall back to their template default.

Examples:
  crewship crew apply-avatar-style my-crew --style bottts-neutral
  crewship crew apply-avatar-style my-crew --reset`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		style, _ := cmd.Flags().GetString("style")
		reset, _ := cmd.Flags().GetBool("reset")
		if reset && style != "" {
			return fmt.Errorf("--style and --reset are mutually exclusive")
		}
		if !reset && style == "" {
			return fmt.Errorf("either --style or --reset is required")
		}

		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]any{}
		if reset {
			body["reset_overrides"] = true
		} else {
			body["avatar_style"] = style
		}

		var result struct {
			Updated int64  `json:"updated"`
			Reset   bool   `json:"reset"`
			Style   string `json:"style"`
		}
		if err := postJSON(client, "/api/v1/crews/"+crewID+"/apply-avatar-style", body, &result); err != nil {
			return err
		}

		pairs := [][]string{
			{"Crew", args[0]},
			{"Agents Updated", fmt.Sprintf("%d", result.Updated)},
		}
		if result.Reset {
			pairs = append(pairs, []string{"Reset", "true"})
			cli.PrintSuccess(fmt.Sprintf("Avatar style overrides reset for %d agent(s) in %q.", result.Updated, args[0]))
		} else {
			pairs = append(pairs, []string{"Style", result.Style})
			cli.PrintSuccess(fmt.Sprintf("Avatar style %q applied to %d agent(s) in %q.", result.Style, result.Updated, args[0]))
		}
		return newFormatter().AutoDetail(result, pairs)
	},
}

func init() {
	crewApplyAvatarStyleCmd.Flags().String("style", "", "Avatar style: bottts-neutral|adventurer|fun-emoji|pixel-art|micah|notionists|thumbs|lorelei|big-smile|avataaars")
	crewApplyAvatarStyleCmd.Flags().Bool("reset", false, "Clear avatar_style overrides for every agent in the crew")
	crewCmd.AddCommand(crewApplyAvatarStyleCmd)
}
