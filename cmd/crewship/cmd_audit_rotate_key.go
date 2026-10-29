package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func newAuditRotateKeyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "rotate-key", Short: "Add a new active audit signing key; earlier keys stay for verification", Long: `Add a fresh random active signing key to the server's audit keyring
(<data-dir>/audit-keyring.json, sealed at rest). Every earlier key is kept
for verification only; no historical MAC is rewritten. The rotation is
recorded as an audit.signing_key_rotated instance audit entry.

The running server keeps signing with the keyring it booted with: the new key
signs from the next server start. A keyring configured outside the data
directory (CREWSHIP_AUDIT_KEYRING_PATH) is not changed; rotate it there.
Requires the current instance OWNER with instance:admin.`, Args: cobra.NoArgs}
	cmd.Flags().String("reason", "", "Explicit reason (8–512 characters)")
	cmd.Flags().String("expected-active-key-id", "", "Refuse unless this is the current active key ID")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		reason, _ := cmd.Flags().GetString("reason")
		expected, _ := cmd.Flags().GetString("expected-active-key-id")
		reason = strings.TrimSpace(reason)
		if len(reason) < 8 || len(reason) > 512 {
			return fmt.Errorf("a reason of 8–512 characters is required")
		}
		client := newAPIClient()
		client.WorkspaceID = ""
		body := map[string]any{"reason": reason}
		if expected != "" {
			body["expected_active_key_id"] = expected
		}
		response, err := client.Post("/api/v1/admin/audit/signing-keys/rotate", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(response); err != nil {
			return err
		}
		var result struct {
			PreviousActiveKeyID string `json:"previous_active_key_id" yaml:"previous_active_key_id"`
			ActiveKeyID         string `json:"active_key_id" yaml:"active_key_id"`
			Effective           string `json:"effective" yaml:"effective"`
		}
		if err := cli.ReadJSON(response, &result); err != nil {
			return err
		}
		if result.ActiveKeyID == "" || result.ActiveKeyID == result.PreviousActiveKeyID {
			return fmt.Errorf("server did not acknowledge a new active key")
		}
		return newFormatter().AutoHuman(result, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "New active audit signing key %s (previous %s is kept for verification). Restart the server to sign with it.\n", result.ActiveKeyID, result.PreviousActiveKeyID)
		})
	}
	return cmd
}

func init() { auditCmd.AddCommand(newAuditRotateKeyCommand()) }
