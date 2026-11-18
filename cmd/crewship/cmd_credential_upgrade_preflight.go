package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

type credentialUpgradeInspection struct {
	Database, Workspace, Sandbox, SourceBuild string
	Keeper, ConfigurationKnown                bool
}

// A full candidate distribution supplies the local inspector. Agent clionly
// distributions must not pretend to implement offline database inspection.
var inspectCredentialUpgrade func(context.Context, credentialUpgradeInspection) (any, error)

func newCredentialUpgradePreflightCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "upgrade-preflight", Short: "Inspect retained credential metadata before upgrading", Long: "Read a retained local SQLite database with the candidate full-server binary. No migrations, decryption, network requests or credential changes. Report potential alias conflicts and legacy Codex configuration; this is not authentication or runtime readiness.", Args: cobra.NoArgs, PersistentPreRunE: func(*cobra.Command, []string) error { return nil }}
	cmd.Flags().String("database", "", "Retained local SQLite database (opened read-only; full-server distribution required)")
	cmd.Flags().String("codex-sandbox-mode", "", "Explicit running server setting, including an explicitly empty value when unset")
	cmd.Flags().Bool("keeper-enabled", false, "Explicit effective Keeper setting of the running server")
	cmd.Flags().String("source-build", "", "Verified running server source revision (operator assertion)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if inspectCredentialUpgrade == nil {
			return fmt.Errorf("offline upgrade inspection requires the candidate full-server Crewship distribution, not the agent CLI-only binary")
		}
		database, _ := cmd.Flags().GetString("database")
		if database == "" || flagWorkspace == "" {
			return fmt.Errorf("--database and explicit --workspace are required for offline inspection")
		}
		sandbox, _ := cmd.Flags().GetString("codex-sandbox-mode")
		if sandbox != "" && sandbox != "read-only" && sandbox != "workspace-write" && sandbox != "danger-full-access" {
			return fmt.Errorf("unsupported --codex-sandbox-mode")
		}
		keeper, _ := cmd.Flags().GetBool("keeper-enabled")
		source, _ := cmd.Flags().GetString("source-build")
		if source != "" {
			if raw, err := hex.DecodeString(source); err != nil || len(raw) != 20 {
				return fmt.Errorf("--source-build must be the full verified source revision")
			}
		}
		result, err := inspectCredentialUpgrade(cmd.Context(), credentialUpgradeInspection{database, flagWorkspace, sandbox, source, keeper, cmd.Flags().Changed("codex-sandbox-mode") && cmd.Flags().Changed("keeper-enabled") && source != ""})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	return cmd
}

func init() { credentialCmd.AddCommand(newCredentialUpgradePreflightCommand()) }
