//go:build !clionly

package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// adminReencryptCmd drives POST /api/v1/admin/reencrypt — the server-side
// walk that re-encrypts every stored secret envelope to the current master
// key version. CLI parity for the endpoint (repo rule: every API endpoint
// ships with its command).
//
// HTTP-backed like `admin prune-legacy`: the envelopes live in the running
// server's database and the CURRENT key lives in the server's environment,
// so this must execute inside the server process, not against a local DB.
var adminReencryptCmd = &cobra.Command{
	Use:   "reencrypt",
	Short: "Re-encrypt all stored secrets to the current master key version (admin)",
	Long: `Re-encrypt every stored secret envelope (credentials, OAuth tokens,
webhook secrets, Composio keys, PKCE verifiers, credential escalations)
to the server's CURRENT encryption key version.

This is step 3 of a master-key rotation:

  1. Generate a new key:            openssl rand -hex 32
  2. On the server, set ENCRYPTION_KEY_V2=<new key> and
     CREWSHIP_ENCRYPTION_KEY_VERSION=v2 (keep ENCRYPTION_KEY = old key),
     then restart crewship.
  3. Run:                           crewship admin reencrypt
  4. When it reports failed: 0, the old key is no longer referenced by
     any row and can be retired from the environment.

The operation is idempotent — envelopes already at the current version are
skipped — so re-running after an interruption only processes the remainder.
Values that cannot be decrypted with any configured key are counted under
"failed" and left untouched; the command exits non-zero in that case so a
scripted rotation never retires the old key on a false success.

Examples:
  crewship admin reencrypt
  crewship admin reencrypt --format json | jq .failed`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/admin/reencrypt", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			KeyVersion  string `json:"key_version"`
			Reencrypted int    `json:"reencrypted"`
			Skipped     int    `json:"skipped"`
			Failed      int    `json:"failed"`
			Columns     []struct {
				Table       string `json:"table"`
				Column      string `json:"column"`
				Reencrypted int    `json:"reencrypted"`
				Skipped     int    `json:"skipped"`
				Failed      int    `json:"failed"`
			} `json:"columns"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		if err := newFormatter().AutoHuman(out, func() {
			fmt.Printf("%sRe-encryption to key version %s%s\n", cli.Bold, out.KeyVersion, cli.Reset)
			fmt.Printf("  Re-encrypted: %d\n", out.Reencrypted)
			fmt.Printf("  Skipped:      %d (already current, or empty)\n", out.Skipped)
			fmt.Printf("  Failed:       %d\n", out.Failed)
			if len(out.Columns) > 0 {
				fmt.Printf("\n  %-25s %-25s %12s %8s %7s\n", "TABLE", "COLUMN", "REENCRYPTED", "SKIPPED", "FAILED")
				for _, c := range out.Columns {
					fmt.Printf("  %-25s %-25s %12d %8d %7d\n", c.Table, c.Column, c.Reencrypted, c.Skipped, c.Failed)
				}
			}
		}); err != nil {
			return err
		}

		if out.Failed > 0 {
			// Non-zero exit: rows remain on an old/unknown key. Retiring the
			// old key now would strand them permanently.
			return fmt.Errorf("%d value(s) could not be re-encrypted (undecryptable with configured keys) — do NOT retire the old key; see server logs for the affected rows", out.Failed)
		}
		return nil
	},
}

func init() {
	adminCmd.AddCommand(adminReencryptCmd)
}
