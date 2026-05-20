package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// cmd_instance.go — `crewship instance settings ...` admin commands.
//
// Wraps the /api/v1/instance/settings handler from
// internal/api/instance_settings_handler.go. Mirrors the shape of
// cmd_label.go (cobra subcommand tree + cli.Client + cli.Formatter).
//
// Registration in main.go is intentionally NOT done here per SPEC-2
// implementation contract — the wiring agent runs after every parallel
// agent finishes and is the only file that touches main.go.

// instanceSettingItem is the wire shape returned by the handler's
// instanceSetting struct (key/value/updated_at).
type instanceSettingItem struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ── Top-level: `crewship instance ...` ─────────────────────────────────

var instanceCmd = &cobra.Command{
	Use:   "instance",
	Short: "Manage instance-wide configuration",
	Long: `Instance-level admin commands.

Instance config lives in the singleton app_settings table and is
shared across every workspace on this crewship binary. Most users
do not need this command; it exists for the operator who runs the
` + "`crewship start`" + ` daemon.`,
}

// ── Sub-group: `crewship instance settings ...` ────────────────────────

var instanceSettingsCmd = &cobra.Command{
	Use:     "settings",
	Aliases: []string{"setting"},
	Short:   "Read and write instance settings",
}

// ── settings list ──────────────────────────────────────────────────────

var instanceSettingsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every instance setting key/value pair",
	Long: `List every instance setting.

Sensitive values (any matching prefixes ` + "`smtp.password`" + `,
` + "`oauth.*.client_secret`" + `, ` + "`webhook.*.secret`" + `) are
redacted to ` + "`***`" + ` server-side — they're write-only on read.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		// No workspace requirement: the route is workspace-scoped only
		// for the RBAC check, not for filtering rows. The server picks
		// the caller's resolved workspace via wsCtx middleware.
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/instance/settings")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var settings []instanceSettingItem
		if err := cli.ReadJSON(resp, &settings); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"KEY", "VALUE", "UPDATED"}
		rows := make([][]string, 0, len(settings))
		for _, s := range settings {
			rows = append(rows, []string{s.Key, s.Value, s.UpdatedAt})
		}
		return f.Auto(settings, headers, rows)
	},
}

// ── settings get <key> ─────────────────────────────────────────────────

var instanceSettingsGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Read one instance setting",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		key := args[0]
		if key == "" {
			return fmt.Errorf("<key> is required")
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/instance/settings/" + key)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var s instanceSettingItem
		if err := cli.ReadJSON(resp, &s); err != nil {
			return err
		}

		f := newFormatter()
		pairs := [][]string{
			{"Key", s.Key},
			{"Value", s.Value},
			{"Updated", s.UpdatedAt},
		}
		return f.AutoDetail(s, pairs)
	},
}

// ── settings set <key> <value> ─────────────────────────────────────────

var instanceSettingsSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Upsert an instance setting",
	Long: `Upsert an instance setting.

Sensitive keys (` + "`smtp.password`" + `, ` + "`oauth.*.client_secret`" + `,
` + "`webhook.*.secret`" + `) are written through verbatim but the echo
response redacts the value to ` + "`***`" + `. Confirm via a separate
verification path (e.g. the dependent service successfully connects)
rather than reading the value back.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		key, value := args[0], args[1]
		if key == "" {
			return fmt.Errorf("<key> is required")
		}

		body := map[string]string{"value": value}

		client := newAPIClient()
		// cli.Client has no PUT helper; fall through to Do() which all
		// the other verbs (Get/Post/Patch/Delete) wrap.
		resp, err := client.Do("PUT", "/api/v1/instance/settings/"+key, body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var s instanceSettingItem
		if err := cli.ReadJSON(resp, &s); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Setting saved: %s = %s", s.Key, s.Value))
		return nil
	},
}

// ── settings delete <key> ──────────────────────────────────────────────

var instanceSettingsDeleteCmd = &cobra.Command{
	Use:     "delete <key>",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete an instance setting",
	Long: `Delete an instance setting.

A small set of bootstrap keys (` + "`instance.bootstrap_at`" + `,
` + "`instance.first_user_id`" + `, ` + "`schema.version`" + `) are
rejected server-side with 403 — removing them would break re-bootstrap
on the next restart.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		key := args[0]
		if key == "" {
			return fmt.Errorf("<key> is required")
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete instance setting %q?", key)); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/instance/settings/" + key)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Setting deleted.")
		return nil
	},
}

func init() {
	instanceSettingsDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	instanceSettingsCmd.AddCommand(instanceSettingsListCmd)
	instanceSettingsCmd.AddCommand(instanceSettingsGetCmd)
	instanceSettingsCmd.AddCommand(instanceSettingsSetCmd)
	instanceSettingsCmd.AddCommand(instanceSettingsDeleteCmd)

	instanceCmd.AddCommand(instanceSettingsCmd)
}
