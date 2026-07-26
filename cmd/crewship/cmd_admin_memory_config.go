//go:build !clionly

package main

// `crewship admin memory-config` — read + adjust workspaces.memory_config
// (#1379).
//
// The endpoint has existed since the memory-hardening series (Iter 6) so
// operators could change memory retention without editing SQLite by hand. It
// had no CLI and nothing rendered it, so in practice the hand-edit was still
// the only route — which is exactly what the endpoint was built to avoid.
//
// Deliberately a partial PATCH: the server merges the patch into the stored
// document and preserves unknown keys, so a future setting is not clobbered by
// an older CLI writing a whole document back.

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// memoryConfigRow mirrors internal/api.memoryConfigResponse.
type memoryConfigRow struct {
	WorkspaceID string `json:"workspace_id"`
	// VersionsRetentionDays is the RESOLVED value — the stored setting when
	// present, otherwise the built-in default. IsDefault says which.
	VersionsRetentionDays int     `json:"versions_retention_days"`
	IsDefault             bool    `json:"is_default"`
	RawConfig             *string `json:"raw_config"`
}

var adminMemoryConfigCmd = &cobra.Command{
	Use:     "memory-config",
	Aliases: []string{"memcfg"},
	Short:   "Read or adjust the workspace's memory configuration (admin)",
	Long: `Read and adjust workspaces.memory_config — currently the retention window
for memory_versions rows, consumed by the per-workspace retention sweep.

Reading is ADMIN+; writing is manage-tier and emits memory.config_updated to
the journal, so a compliance audit can trace when retention policy changed and
who changed it.

Examples:
  crewship admin memory-config get
  crewship admin memory-config get --format json
  crewship admin memory-config set --retention-days 30`,
}

var adminMemoryConfigGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the workspace's resolved memory configuration",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/memory/config")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row memoryConfigRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return resolvedFormatter(cmd).AutoHuman(row, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SETTING\tVALUE\tSOURCE")
			// "30 days" alone doesn't tell an operator whether anyone chose it.
			// That matters before changing it: overriding a default is routine,
			// overriding somebody's deliberate policy is not.
			source := "explicit (set for this workspace)"
			if row.IsDefault {
				source = "default (not set for this workspace)"
			}
			fmt.Fprintf(w, "Memory versions retention\t%d days\t%s\n", row.VersionsRetentionDays, source)
			if err := w.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
			// Surface unknown keys rather than hiding them: the stored document
			// can carry settings this CLI version does not model, and silently
			// omitting them would make `get` look authoritative when it isn't.
			if row.RawConfig != nil && *row.RawConfig != "" && *row.RawConfig != "{}" {
				fmt.Printf("\nStored document:\n%s\n", prettyJSON(*row.RawConfig))
			}
		})
	},
}

var adminMemoryConfigSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the workspace's memory retention window (ADMIN)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if !cmd.Flags().Changed("retention-days") {
			return fmt.Errorf("--retention-days is required (e.g. --retention-days 30)")
		}
		days, _ := cmd.Flags().GetInt("retention-days")

		client := newAPIClient()
		// PATCH, not PUT: the server merges this into the stored document and
		// keeps keys this CLI doesn't know about. Sending a whole document
		// would let an older binary silently drop a newer setting.
		resp, err := client.Patch("/api/v1/admin/memory/config",
			map[string]any{"versions_retention_days": days})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row memoryConfigRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		cli.PrintSuccess(fmt.Sprintf("Memory versions retention set to %d days.", row.VersionsRetentionDays))
		fmt.Println("The per-workspace retention sweep uses the new window on its next pass;")
		fmt.Println("memory_versions rows already older than it become eligible for deletion.")
		return nil
	},
}

func init() {
	adminMemoryConfigSetCmd.Flags().Int("retention-days", 0,
		"How long to keep memory_versions rows, in days (1..3650) — REQUIRED")

	adminMemoryConfigCmd.AddCommand(adminMemoryConfigGetCmd)
	adminMemoryConfigCmd.AddCommand(adminMemoryConfigSetCmd)
	adminCmd.AddCommand(adminMemoryConfigCmd)
}
