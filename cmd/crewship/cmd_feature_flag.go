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
	ID              string  `json:"id" yaml:"id"`
	Key             string  `json:"key" yaml:"key"`
	Description     *string `json:"description" yaml:"description"`
	Enabled         bool    `json:"enabled" yaml:"enabled"`
	Percentage      int     `json:"percentage" yaml:"percentage"`
	CreatedAt       string  `json:"created_at" yaml:"created_at"`
	UpdatedAt       string  `json:"updated_at" yaml:"updated_at"`
	OverrideEnabled *bool   `json:"override_enabled,omitempty" yaml:"override_enabled,omitempty"`
}

var featureFlagCmd = &cobra.Command{
	Use:     "feature-flag",
	Aliases: []string{"flag"},
	Short:   "Manage feature flags and per-workspace overrides",
	Long: `Feature flags are instance-wide definitions (key, default, description)
plus an optional on/off override per workspace. The effective value for a
workspace is its override when one exists, otherwise the definition's default.
A key with no definition is off. The stored percentage is not evaluated.

Who can do what:
  list                       any member of the workspace
  enable, disable, inherit   workspace OWNER or ADMIN
  create, update, delete     instance administrator who is also OWNER or
                             ADMIN of the selected workspace

Workspace flags for Artifacts (all default off; turn on per workspace with
"crewship feature-flag enable <key>"):
  artifact_projects          multi-screen HTML Project artifacts
  artifact_dashboard_data    Dashboard datasets; also needs artifact_projects
  artifact_slides            Slides views and PDF export; also needs
                             artifact_projects
  artifacts_design           Design artifacts
  gridcraft_editor           live workbook editing; also needs the server
                             to run the workbook engine with a qualified
                             worker and editor: gridcraft.enabled and
                             gridcraft.editor_enabled plus the worker
                             service socket and token file, the editor
                             origin and the editor bundle settings. With only
                             the two enable switches the server refuses to
                             start. Restart the server after changing them

An operator can force the first four on for every workspace with the server
environment variables CREWSHIP_ARTIFACT_PROJECTS_ENABLED,
CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED, CREWSHIP_ARTIFACT_SLIDES_ENABLED
and CREWSHIP_ARTIFACTS_DESIGN_ENABLED set to "true"; "list" does not show that.

The web screens for these artifacts also need build-time switches of the web
app, set to "true" when the web app is built (default off, rebuild required;
this command does not change them):
  artifact_projects          NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS (cards and
                             viewer in chat) and NEXT_PUBLIC_ARTIFACTS_LIBRARY
                             (the /artifacts library)
  artifact_dashboard_data    NEXT_PUBLIC_ARTIFACT_DASHBOARDS
  artifact_slides            NEXT_PUBLIC_ARTIFACT_SLIDES
  artifacts_design           NEXT_PUBLIC_ARTIFACTS_DESIGN
Prebuilt releases ship with these off. To turn them on, build from source
with the variables exported, for example
NEXT_PUBLIC_ARTIFACTS_LIBRARY=true make build; exported variables override
the bundled .env.production, which sets NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS=false.

Server settings and environment variables, including the gridcraft ones:
https://docs.crewship.ai/configuration/environment`,
	Example: `  crewship feature-flag list
  crewship feature-flag enable artifact_projects
  crewship flag inherit artifact_projects`,
}

var featureFlagListCmd = &cobra.Command{
	Use:   "list",
	Short: "List feature flags + this workspace's overrides",
	Long: `List shows every flag definition with its instance default (DEFAULT),
this workspace's override (WORKSPACE: on, off or inherit) and the value the
server uses for this workspace (EFFECTIVE). Any workspace member can list.

With -f json, yaml or ndjson each flag is printed as an object with id, key,
description, enabled (the default), percentage, created_at, updated_at and,
only when this workspace has an override, override_enabled. -f quiet prints
the keys only.`,
	Example: `  crewship feature-flag list
  crewship feature-flag list -f json
  crewship flag list -f quiet`,
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

The flag is instance-global: it becomes visible to every workspace, not just
the one the CLI is currently pointed at. The server requires an instance
administrator who is also OWNER or ADMIN of the selected workspace; anyone
else gets 403. A duplicate key returns 409.

--percentage is stored and shown by "list", but the server does not use it
when it decides whether a flag is on.`,
	Example: `  crewship feature-flag create --key provisioner-v2 --description "v2 provisioner"
  crewship feature-flag create --key provisioner-v2 --enabled -f json`,
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

		return resolvedFormatter(cmd).AutoHuman(ff, func() {
			cli.PrintSuccess(fmt.Sprintf("Feature flag %q created (default %s, %d%% rollout).",
				ff.Key, boolBadge(ff.Enabled), ff.Percentage))
		})
	},
}

var featureFlagUpdateCmd = &cobra.Command{
	Use:   "update <key>",
	Short: "Update a feature flag definition (PATCH /api/v1/feature-flags/{key})",
	Long: `Update changes one or more fields of an existing flag DEFINITION — the
instance-wide default, not this workspace's override. Only flags explicitly
passed are sent, so omitting a flag leaves its current value untouched.

Use "crewship feature-flag enable/disable <key>" instead if you only want to
flip the effective value for the current workspace.

Requires an instance administrator who is also OWNER or ADMIN of the
selected workspace (403 otherwise; 404 for an unknown key). Prints a
one-line confirmation; the global -f/--format flag does not change it.`,
	Example: `  crewship feature-flag update provisioner-v2 --enabled
  crewship feature-flag update provisioner-v2 --description "now the default"`,
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
	Long: `Enable sets this workspace's override to on, whatever the instance
default is. Other workspaces are not affected. Requires workspace OWNER or
ADMIN (403 otherwise). The flag definition must exist (404 otherwise); a
key with no definition cannot be overridden.

Prints a one-line confirmation; the global -f/--format flag does not change it.`,
	Example: `  crewship feature-flag enable artifact_projects
  crewship feature-flag enable artifacts_design`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setOverride(args[0], true)
	},
}

var featureFlagDisableCmd = &cobra.Command{
	Use:   "disable <key>",
	Short: "Disable a feature flag for the current workspace (PUT override)",
	Long: `Disable sets this workspace's override to off, even when the instance
default is on. Other workspaces are not affected. Requires workspace OWNER
or ADMIN (403 otherwise). The flag definition must exist (404 otherwise).

Prints a one-line confirmation; the global -f/--format flag does not change it.`,
	Example: `  crewship feature-flag disable artifact_slides
  crewship feature-flag disable run_verdict_summaries`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setOverride(args[0], false)
	},
}

var featureFlagInheritCmd = &cobra.Command{
	Use:   "inherit <key>",
	Short: "Drop this workspace's override and revert to the instance default",
	Long: `Inherit removes this workspace's override, so the workspace uses the
definition's default again (off for the Artifacts flags). Running it when
there is no override succeeds and changes nothing. Requires workspace OWNER
or ADMIN (403 otherwise); an unknown key returns 404.

Prints a one-line confirmation; the global -f/--format flag does not change it.`,
	Example: `  crewship feature-flag inherit artifact_projects`,
	Args:    cobra.ExactArgs(1),
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
the flag, instance-wide. Requires an instance administrator who is also
OWNER or ADMIN of the selected workspace (403 otherwise).

Without --yes the command asks for confirmation and aborts when there is no
answer. Prints a one-line confirmation; the global -f/--format flag does not
change it.

Use "crewship feature-flag inherit <key>" instead if you only want to
drop this workspace's override and fall back to the instance default.`,
	Example: `  crewship feature-flag delete provisioner-v2
  crewship feature-flag delete provisioner-v2 --yes`,
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
	featureFlagCreateCmd.Flags().Int("percentage", 0, "Rollout percentage, 0-100; stored and listed, not evaluated by the server")
	_ = featureFlagCreateCmd.MarkFlagRequired("key")

	featureFlagUpdateCmd.Flags().String("description", "", "New description (pass \"\" to clear)")
	featureFlagUpdateCmd.Flags().Bool("enabled", false, "New instance default value")
	featureFlagUpdateCmd.Flags().Int("percentage", 0, "New rollout percentage, 0-100; stored and listed, not evaluated by the server")

	featureFlagCmd.AddCommand(featureFlagListCmd)
	featureFlagCmd.AddCommand(featureFlagCreateCmd)
	featureFlagCmd.AddCommand(featureFlagUpdateCmd)
	featureFlagCmd.AddCommand(featureFlagEnableCmd)
	featureFlagCmd.AddCommand(featureFlagDisableCmd)
	featureFlagCmd.AddCommand(featureFlagInheritCmd)
	featureFlagCmd.AddCommand(featureFlagDeleteCmd)
}
