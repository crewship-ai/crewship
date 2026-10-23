//go:build !clionly

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/journal"
)

// newAuditExportKeyringCommand writes the sealed audit keyring out as
// plaintext for a rollback to a build that predates sealing. Host-local by
// design, like `admin reset-password`: the keys never cross the API, and
// whoever can run it already holds the data directory and its master key.
func newAuditExportKeyringCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "export-keyring", Short: "Write the audit keyring as plaintext for a downgrade (host-local)", Long: `Decrypt this host's audit keyring and write it, unsealed, to a new file.

Older builds cannot read the sealed keyring. Before rolling back across the
release that introduced sealing: stop the server, export, put the exported
file in place of <data-dir>/audit-keyring.json (or point
CREWSHIP_AUDIT_KEYRING_PATH at it), then start the older build. A newer build
seals the plaintext file again at its next start.

The output holds every audit signing and verification key in the clear: it
is created with mode 0600 and never overwrites an existing file. --plaintext
is required to say so. The keyring is found as for ` + "`admin reset-password`" + `.`, Args: cobra.NoArgs}
	cmd.Flags().Bool("plaintext", false, "Required: acknowledge that the output contains the keys unencrypted")
	cmd.Flags().String("output", "", "New file to write (must not exist)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		plaintext, _ := cmd.Flags().GetBool("plaintext")
		output, _ := cmd.Flags().GetString("output")
		if !plaintext || output == "" {
			return errors.New("--plaintext and --output are required: the export holds the audit keys unencrypted")
		}
		target, err := resolveLocalDBTarget()
		if err != nil {
			return err
		}
		path, err := localAuditKeyringPath(target)
		if err != nil {
			return err
		}
		if err := adoptPersistedMasterKeys(filepath.Dir(path)); err != nil {
			return err
		}
		keys, err := journal.LoadSigningKeys(path)
		if err != nil {
			return fmt.Errorf("audit keyring %s: %w", path, err)
		}
		raw, err := journal.ExportSigningKeys(keys)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create %s: %w", output, err)
		}
		if _, err := file.Write(raw); err != nil {
			file.Close()
			os.Remove(output)
			return err
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			os.Remove(output)
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Wrote the audit keyring from %s UNENCRYPTED to %s (mode 0600). Delete it once the downgrade is done.", path, output))
		return nil
	}
	return cmd
}

func init() { auditCmd.AddCommand(newAuditExportKeyringCommand()) }
