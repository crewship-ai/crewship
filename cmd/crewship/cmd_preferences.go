package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Personal, per-user UI preferences — the CLI side of GET/PUT/DELETE
// /api/v1/me/preferences. These are scoped to the calling user (not the
// workspace) and need no elevated role: the endpoints authenticate the
// caller and key every row on the user_id from the auth context, so
// requireAuth (not requireAuthAndWorkspace) is the right gate.
var preferencesCmd = &cobra.Command{
	Use:     "preferences",
	Aliases: []string{"prefs", "preference"},
	Short:   "Manage your personal UI preferences (per-user, not workspace-scoped)",
	Long: `Read and write your own user preferences — small JSON settings the
dashboard persists per user (theme, layout toggles, ...). They are scoped
to your account, not the workspace, so no elevated role is required.

Values are stored as raw JSON, so 'set' takes a JSON literal:
  crewship preferences set theme '"dark"'
  crewship preferences set sidebar '{"collapsed":true}'
  crewship preferences set density 3`,
}

var preferencesListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List all your preferences",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/me/preferences")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// The endpoint returns a { key: <raw-json-value> } map.
		var prefs map[string]json.RawMessage
		if err := cli.ReadJSON(resp, &prefs); err != nil {
			return err
		}
		// Stable key order so the table output is deterministic.
		keys := make([]string, 0, len(prefs))
		for k := range prefs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rows := make([][]string, 0, len(keys))
		// Decode each raw value into a concrete Go type for the structured
		// (json/yaml) output. Passing the raw map[string]json.RawMessage
		// straight to the formatter breaks `-f yaml`: yaml.v3 renders a
		// json.RawMessage ([]byte) as a list of byte values, not the value.
		// The table keeps the compact raw-JSON literal.
		decoded := make(map[string]any, len(prefs))
		for _, k := range keys {
			rows = append(rows, []string{k, string(prefs[k])})
			var val any
			if err := json.Unmarshal(prefs[k], &val); err != nil {
				val = string(prefs[k]) // fall back to the literal if undecodable
			}
			decoded[k] = val
		}
		return newFormatter().Auto(decoded, []string{"KEY", "VALUE"}, rows)
	},
}

var preferencesSetCmd = &cobra.Command{
	Use:   "set <key> <json-value>",
	Short: "Set a preference to a JSON value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		key, value := args[0], args[1]
		// The server stores the request body verbatim and rejects a
		// non-JSON value with 400 — validate here too so the user gets a
		// clear, example-bearing error instead of a bare "invalid" from
		// the API.
		if !json.Valid([]byte(value)) {
			return fmt.Errorf("value must be valid JSON — quote strings and booleans, e.g. '\"dark\"', 'true', '{\"collapsed\":true}' (got: %s)", value)
		}
		client := newAPIClient()
		// Send the JSON literal verbatim: json.RawMessage marshals to
		// itself, so the body is exactly `value`. Passing a Go string
		// would double-encode it (the server would store `"true"`, not
		// `true`).
		resp, err := client.Put("/api/v1/me/preferences/"+url.PathEscape(key), json.RawMessage(value))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Preference %q set.", key))
		return nil
	},
}

var preferencesDeleteCmd = &cobra.Command{
	Use:     "delete <key>",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete a preference",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		key := args[0]
		if err := confirmAction(cmd, fmt.Sprintf("Delete preference %q?", key)); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/me/preferences/" + url.PathEscape(key))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Preference %q deleted.", key))
		return nil
	},
}

func init() {
	preferencesDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	preferencesCmd.AddCommand(preferencesListCmd, preferencesSetCmd, preferencesDeleteCmd)
}
