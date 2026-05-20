package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// featureFlagItem mirrors the JSON shape returned by
// GET /api/v1/feature-flags (see internal/api/feature_flags_handler.go
// featureFlagResponse). Decoded into a CLI-only struct so we don't drag
// the internal/api package into cmd/crewship.
type featureFlagItem struct {
	ID              string  `json:"id"`
	Key             string  `json:"key"`
	Description     *string `json:"description"`
	Enabled         bool    `json:"enabled"`
	Percentage      int     `json:"percentage"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	OverrideEnabled *bool   `json:"override_enabled,omitempty"`
}

var featureFlagCmd = &cobra.Command{
	Use:     "feature-flag",
	Aliases: []string{"flag"},
	Short:   "Manage feature flags and per-workspace overrides",
}

var featureFlagListCmd = &cobra.Command{
	Use:   "list",
	Short: "List feature flags + this workspace's overrides",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/feature-flags")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var flags []featureFlagItem
		if err := cli.ReadJSON(resp, &flags); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"KEY", "DEFAULT", "WORKSPACE", "EFFECTIVE", "PERCENT", "DESCRIPTION"}
		rows := make([][]string, 0, len(flags))
		for _, fl := range flags {
			defaultStr := boolBadge(fl.Enabled)
			wsStr := "inherit"
			effective := fl.Enabled
			if fl.OverrideEnabled != nil {
				wsStr = boolBadge(*fl.OverrideEnabled)
				effective = *fl.OverrideEnabled
			}
			desc := derefStr(fl.Description, "-")
			rows = append(rows, []string{
				fl.Key,
				defaultStr,
				wsStr,
				boolBadge(effective),
				fmt.Sprintf("%d%%", fl.Percentage),
				desc,
			})
		}
		return f.Auto(flags, headers, rows)
	},
}

var featureFlagEnableCmd = &cobra.Command{
	Use:   "enable <key>",
	Short: "Enable a feature flag for the current workspace (PUT override)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setOverride(args[0], true)
	},
}

var featureFlagDisableCmd = &cobra.Command{
	Use:   "disable <key>",
	Short: "Disable a feature flag for the current workspace (PUT override)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setOverride(args[0], false)
	},
}

var featureFlagInheritCmd = &cobra.Command{
	Use:   "inherit <key>",
	Short: "Drop this workspace's override and revert to the instance default",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		key := args[0]
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/feature-flags/" + key + "/override")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Override cleared for %q — workspace will use the instance default.", key))
		return nil
	},
}

// setOverride PUTs a boolean override for the current workspace. Shared by
// the `enable` and `disable` subcommands because they only differ in the
// body's boolean value.
func setOverride(key string, enabled bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}

	client := newAPIClient()
	// internal/cli.Client.Do covers all verbs — there's no .Put method,
	// but Do("PUT", …) serializes the body via the same JSON pipeline as
	// Post/Patch, so we go through the generic call directly.
	resp, err := client.Do("PUT", "/api/v1/feature-flags/"+key+"/override",
		map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}

	verb := "enabled"
	if !enabled {
		verb = "disabled"
	}
	cli.PrintSuccess(fmt.Sprintf("Feature flag %q %s for current workspace.", key, verb))
	return nil
}

// boolBadge maps booleans to a compact column-friendly display.
func boolBadge(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func init() {
	featureFlagCmd.AddCommand(featureFlagListCmd)
	featureFlagCmd.AddCommand(featureFlagEnableCmd)
	featureFlagCmd.AddCommand(featureFlagDisableCmd)
	featureFlagCmd.AddCommand(featureFlagInheritCmd)
}
