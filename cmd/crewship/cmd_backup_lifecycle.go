package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/cli"
)

var backupCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new backup bundle",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		scope, _ := cmd.Flags().GetString("scope")
		crewRef, _ := cmd.Flags().GetString("crew")
		noEncrypt, _ := cmd.Flags().GetBool("no-encrypt")
		passphraseFile, _ := cmd.Flags().GetString("passphrase-file")
		recipient, _ := cmd.Flags().GetString("recipient")
		useKeyring, _ := cmd.Flags().GetBool("use-keyring")

		if scope != "workspace" && scope != "crew" {
			return fmt.Errorf("--scope must be 'workspace' or 'crew' (got %q)", scope)
		}
		if scope == "crew" && crewRef == "" {
			return fmt.Errorf("--crew <slug-or-id> is required when --scope=crew")
		}

		// Mutually-exclusive encryption selectors. --recipient overrides
		// --passphrase-file; --no-encrypt wins over both and skips the
		// prompt entirely.
		if recipient != "" && noEncrypt {
			return fmt.Errorf("--recipient and --no-encrypt are mutually exclusive")
		}
		if recipient != "" && passphraseFile != "" {
			return fmt.Errorf("--recipient and --passphrase-file are mutually exclusive")
		}

		var passphrase string
		switch {
		case noEncrypt:
			cli.PrintWarning("--no-encrypt: bundle will contain plaintext data. Protect it accordingly.")
		case recipient != "":
			if !strings.HasPrefix(recipient, "age1") {
				return fmt.Errorf("--recipient must be an age1… public key")
			}
			// Recipient is packed into the request body below as its
			// own JSON field; leave passphrase empty.
		default:
			// Keyring lookup short-circuits the prompt when the admin
			// asked for --use-keyring and we have a stored passphrase
			// for this workspace. Wrong keyring content surfaces as a
			// decryption failure during restore — the bundle itself is
			// still written with whatever passphrase the keyring held.
			ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
			// --use-keyring is an explicit user opt-in; surface
			// init/decrypt/write failures instead of silently degrading
			// to a prompt. The one error we DO swallow is
			// ErrKeyringEntryNotFound — that's the "first use on this
			// workspace" path where a fresh prompt is the correct
			// behaviour.
			var fromKeyring bool
			if useKeyring && passphraseFile == "" {
				kr, err := backup.DefaultKeyring(cmd.Context())
				if err != nil {
					return fmt.Errorf("open backup keyring: %w", err)
				}
				p, err := kr.GetPassphrase(cmd.Context(), ws)
				switch {
				case err == nil:
					passphrase = p
					fromKeyring = true
				case errors.Is(err, backup.ErrKeyringEntryNotFound):
					// fall through to the prompt below
				default:
					return fmt.Errorf("read backup keyring: %w", err)
				}
			}
			if passphrase == "" {
				p, err := readPassphrase(passphraseFile, true /*confirm*/)
				if err != nil {
					return err
				}
				passphrase = p
			}
			// Only persist AFTER the user confirmed a fresh prompt —
			// fromKeyring suppresses the re-write when the passphrase
			// came straight out of the keyring (re-encrypting the same
			// value just burns entropy and churns the file).
			// Store failures are reported as warnings rather than
			// aborting: the bundle is still going to be written, and
			// losing the keyring cache is recoverable at next use.
			if useKeyring && passphraseFile == "" && ws != "" && !fromKeyring {
				kr, err := backup.DefaultKeyring(cmd.Context())
				if err != nil {
					cli.PrintWarning(fmt.Sprintf("Keyring unavailable: %v", err))
				} else if err := kr.StorePassphrase(cmd.Context(), ws, passphrase); err != nil {
					cli.PrintWarning(fmt.Sprintf("Failed to store passphrase in keyring: %v", err))
				}
			}
		}

		// Resolve crew slug → ID if necessary.
		client := newAPIClient()
		var crewID string
		if scope == "crew" {
			var err error
			crewID, err = resolveCrewID(client, crewRef)
			if err != nil {
				return err
			}
		}

		outputDir, _ := cmd.Flags().GetString("output")
		body := map[string]any{
			"scope":      scope,
			"crew_id":    crewID,
			"passphrase": passphrase,
			"recipient":  recipient,
			"no_encrypt": noEncrypt,
			"output_dir": outputDir,
		}
		resp, err := client.Post("/api/v1/admin/backups", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Path          string `json:"path"`
			Size          int64  `json:"size_bytes"`
			SHA256        string `json:"payload_sha256"`
			FormatVersion int    `json:"format_version"`
			Scope         string `json:"scope"`
			Encrypted     bool   `json:"encrypted"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Backup created: %s", out.Path))
		f := newFormatter()
		headers := []string{"SCOPE", "SIZE", "ENCRYPTED", "FORMAT", "SHA256"}
		rows := [][]string{{
			out.Scope,
			formatBytes(out.Size),
			yesNo(out.Encrypted),
			fmt.Sprintf("v%d", out.FormatVersion),
			truncateLong(out.SHA256, 20),
		}}
		f.Table(headers, rows)
		return nil
	},
}

var backupRestoreCmd = &cobra.Command{
	Use:   "restore <file>",
	Short: "Restore a workspace or crew from a backup bundle",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		asWorkspace, _ := cmd.Flags().GetString("as-workspace")
		asCrew, _ := cmd.Flags().GetString("as-crew")
		passphraseFile, _ := cmd.Flags().GetString("passphrase-file")
		useKeyring, _ := cmd.Flags().GetBool("use-keyring")

		// In a non-interactive environment without --passphrase-file we
		// let the caller through with an empty passphrase so unencrypted
		// bundles restore from CI / scripts. The server will surface a
		// 400 if the bundle turns out to be encrypted and no passphrase
		// was supplied — cleaner than "no passphrase on stdin" from us.
		var passphrase string
		ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
		// Mirror the error-propagation policy used during create: the
		// only silent fallback is ErrKeyringEntryNotFound; every other
		// failure aborts so the admin sees the real cause instead of a
		// later "decryption failed" that's hard to diagnose.
		if useKeyring && passphraseFile == "" && ws != "" {
			kr, err := backup.DefaultKeyring(cmd.Context())
			if err != nil {
				return fmt.Errorf("open backup keyring: %w", err)
			}
			p, err := kr.GetPassphrase(cmd.Context(), ws)
			switch {
			case err == nil:
				passphrase = p
			case errors.Is(err, backup.ErrKeyringEntryNotFound):
				// fall through to prompt / stdin
			default:
				return fmt.Errorf("read backup keyring: %w", err)
			}
		}
		if passphrase == "" {
			if passphraseFile == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
				passphrase = ""
			} else {
				p, err := readPassphrase(passphraseFile, false /*no confirm*/)
				if err != nil {
					return err
				}
				passphrase = p
			}
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		body := map[string]any{
			"path":         args[0],
			"passphrase":   passphrase,
			"as_workspace": asWorkspace,
			"as_crew":      asCrew,
			"dry_run":      dryRun,
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/admin/backups/restore", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			RestoredWs          string `json:"restored_ws"`
			RestoredWorkspaceID string `json:"restored_workspace_id"`
			CrewsCount          int    `json:"crews_count"`
			RowsInserted        int    `json:"rows_inserted"`
			DockerPhaseSkipped  bool   `json:"docker_phase_skipped"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		prefix := "Restore complete"
		if dryRun {
			// The admin asked for a verify-only run. No workspace /
			// crew / agent rows changed and the docker phase was
			// skipped; the only side effect is one
			// backup.restore.dry_run row in the audit log so an
			// auditor can see who tested what.
			prefix = "Restore validation complete (dry-run; no workspace/crew data changes applied)"
		}
		msg := fmt.Sprintf(
			"%s — workspace=%s crews=%d rows=%d",
			prefix, out.RestoredWs, out.CrewsCount, out.RowsInserted,
		)
		if out.RestoredWorkspaceID != "" {
			msg += " id=" + out.RestoredWorkspaceID
		}
		cli.PrintSuccess(msg)
		// The docker-phase warning only matters on a real restore —
		// dry-run never touches docker, so surfacing "you still need
		// to provision crews" would mislead the admin into thinking
		// the DB mutated when it did not.
		if !dryRun && out.DockerPhaseSkipped {
			cli.PrintWarning("Docker phase skipped (--as-workspace/--as-crew supplied). Provision the new crews with `crewship crew provision` and re-run restore without the rewrite flag to land container state.")
		}
		return nil
	},
}

var backupDeleteCmd = &cobra.Command{
	Use:   "delete <file>",
	Short: "Delete a backup bundle from disk",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		force, _ := cmd.Flags().GetBool("force")
		// Interactive confirmation unless --force. Silent deletion of
		// a multi-GB bundle from a fat-fingered command is exactly the
		// kind of footgun we want to put behind a speed bump. When
		// stdin isn't a TTY (scripts, CI) we require --force instead
		// of prompting.
		if !force {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("refusing to delete %s without --force in a non-interactive session", args[0])
			}
			fmt.Fprintf(os.Stderr, "Delete backup %s? [y/N] ", args[0])
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line != "y" && line != "yes" {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/admin/backups?path=" + encodeQuery(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess("Backup deleted: " + args[0])
		return nil
	},
}
