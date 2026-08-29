package main

import (
	"fmt"
	"net/url"

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

var featureFlagCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new feature flag definition (POST /api/v1/feature-flags)",
	Long: `Create adds a new instance-global flag definition — the same "key" that
"list" shows in the DEFAULT column and that "enable"/"disable"/"inherit"
target with a per-workspace override.

This is ADMIN-only server-side and instance-global: the flag becomes visible
to every workspace, not just the one the CLI is currently pointed at.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		key, _ := cmd.Flags().GetString("key")
		if key == "" {
			return cli.WithExitCode(fmt.Errorf("--key is required"), cli.ExitValidation)
		}
		enabled, _ := cmd.Flags().GetBool("enabled")
		percentage, _ := cmd.Flags().GetInt("percentage")
		if percentage < 0 || percentage > 100 {
			return cli.WithExitCode(fmt.Errorf("--percentage must be between 0 and 100"), cli.ExitValidation)
		}

		body := map[string]any{
			"key":        key,
			"enabled":    enabled,
			"percentage": percentage,
		}
		if cmd.Flags().Changed("description") {
			desc, _ := cmd.Flags().GetString("description")
			body["description"] = desc
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/feature-flags", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var ff featureFlagItem
		if err := cli.ReadJSON(resp, &ff); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Feature flag %q created (default %s, %d%% rollout).",
			ff.Key, boolBadge(ff.Enabled), ff.Percentage))
		return nil
	},
}

var featureFlagUpdateCmd = &cobra.Command{
	Use:   "update <key>",
	Short: "Update a feature flag definition (PATCH /api/v1/feature-flags/{key})",
	Long: `Update changes one or more fields of an existing flag DEFINITION — the
instance-wide default, not this workspace's override. Only flags explicitly
passed are sent, so omitting a flag leaves its current value untouched.

Use "crewship feature-flag enable/disable <key>" instead if you only want to
flip the effective value for the current workspace.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		key := args[0]
		body := map[string]any{}
		if cmd.Flags().Changed("description") {
			desc, _ := cmd.Flags().GetString("description")
			body["description"] = desc
		}
		if cmd.Flags().Changed("enabled") {
			enabled, _ := cmd.Flags().GetBool("enabled")
			body["enabled"] = enabled
		}
		if cmd.Flags().Changed("percentage") {
			percentage, _ := cmd.Flags().GetInt("percentage")
			if percentage < 0 || percentage > 100 {
				return cli.WithExitCode(fmt.Errorf("--percentage must be between 0 and 100"), cli.ExitValidation)
			}
			body["percentage"] = percentage
		}
		if len(body) == 0 {
			return cli.WithExitCode(fmt.Errorf(
				"nothing to update — pass at least one of --description, --enabled, --percentage"), cli.ExitValidation)
		}

		client := newAPIClient()
		resp, err := client.Patch("/api/v1/feature-flags/"+url.PathEscape(key), body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var ff featureFlagItem
		if err := cli.ReadJSON(resp, &ff); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Feature flag %q updated (default %s, %d%% rollout).",
			ff.Key, boolBadge(ff.Enabled), ff.Percentage))
		return nil
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
		resp, err := client.Delete("/api/v1/feature-flags/" + url.PathEscape(key) + "/override")
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

var featureFlagDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a feature flag definition (DELETE /api/v1/feature-flags/{key})",
	Long: `Delete removes the feature flag *definition* itself — not just this
workspace's override. It cascades to every workspace's override rows for
the flag, instance-wide. ADMIN-only server-side.

Use "crewship feature-flag inherit <key>" instead if you only want to
drop this workspace's override and fall back to the instance default.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		key := args[0]
		if err := confirmAction(cmd, fmt.Sprintf("Delete feature flag %q? This removes the definition and every workspace's override.", key)); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/feature-flags/" + url.PathEscape(key))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Feature flag %q deleted.", key))
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
	resp, err := client.Do("PUT", "/api/v1/feature-flags/"+url.PathEscape(key)+"/override",
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
	featureFlagDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	featureFlagCreateCmd.Flags().String("key", "", "Flag identifier — REQUIRED (e.g. provisioner-v2)")
	featureFlagCreateCmd.Flags().String("description", "", "Free-form description shown in `feature-flag list`")
	featureFlagCreateCmd.Flags().Bool("enabled", false, "Instance default value (a workspace override still wins)")
	featureFlagCreateCmd.Flags().Int("percentage", 0, "Gradual-rollout percentage, 0-100 (bypassed by any override)")
	_ = featureFlagCreateCmd.MarkFlagRequired("key")

	featureFlagUpdateCmd.Flags().String("description", "", "New description (pass \"\" to clear)")
	featureFlagUpdateCmd.Flags().Bool("enabled", false, "New instance default value")
	featureFlagUpdateCmd.Flags().Int("percentage", 0, "New gradual-rollout percentage, 0-100")

	featureFlagCmd.AddCommand(featureFlagListCmd)
	featureFlagCmd.AddCommand(featureFlagCreateCmd)
	featureFlagCmd.AddCommand(featureFlagUpdateCmd)
	featureFlagCmd.AddCommand(featureFlagEnableCmd)
	featureFlagCmd.AddCommand(featureFlagDisableCmd)
	featureFlagCmd.AddCommand(featureFlagInheritCmd)
	featureFlagCmd.AddCommand(featureFlagDeleteCmd)
}
