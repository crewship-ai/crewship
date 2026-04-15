package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/cli"
)

// backupCmd is the root of `crewship backup …`. All backup operations
// are admin-only (OWNER/ADMIN on the workspace) and hit the
// /api/v1/admin/backups endpoints on a running Crewship server.
var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Create, list, inspect and restore workspace / crew backups (admin only)",
	Long: `Manage workspace and crew backups. All subcommands require OWNER or
ADMIN role on the workspace; MEMBER and VIEWER roles are refused.

Bundles live at ~/.crewship/backups by default and are AGE-encrypted
with a passphrase unless --no-encrypt is supplied.

Examples:
  crewship backup create --scope=workspace
  crewship backup create --scope=crew --crew=<slug-or-id>
  crewship backup list
  crewship backup inspect ~/.crewship/backups/crewship-workspace-acme-*.tar.zst
  crewship backup restore ~/.crewship/backups/crewship-workspace-acme-*.tar.zst
  crewship backup delete ~/.crewship/backups/old.tar.zst`,
}

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
			if useKeyring && passphraseFile == "" {
				if kr, kerr := backup.DefaultKeyring(cmd.Context()); kerr == nil {
					if p, gerr := kr.GetPassphrase(cmd.Context(), ws); gerr == nil {
						passphrase = p
					}
				}
			}
			if passphrase == "" {
				p, err := readPassphrase(passphraseFile, true /*confirm*/)
				if err != nil {
					return err
				}
				passphrase = p
			}
			// Only persist AFTER the user confirmed a fresh prompt — a
			// passphrase we just read from the keyring needs no rewrite.
			if useKeyring && passphraseFile == "" && ws != "" {
				if kr, kerr := backup.DefaultKeyring(cmd.Context()); kerr == nil {
					_ = kr.StorePassphrase(cmd.Context(), ws, passphrase)
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

var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List backup bundles in ~/.crewship/backups",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/backups")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Data []struct {
				Path          string `json:"path"`
				FileName      string `json:"file_name"`
				Size          int64  `json:"size_bytes"`
				Scope         string `json:"scope"`
				Encrypted     bool   `json:"encrypted"`
				CreatedAt     string `json:"created_at,omitempty"`
				FormatVersion int    `json:"format_version,omitempty"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if len(out.Data) == 0 {
			fmt.Fprintln(os.Stderr, "No backups found.")
			return nil
		}
		f := newFormatter()
		headers := []string{"FILE", "SCOPE", "SIZE", "ENCRYPTED", "FORMAT", "CREATED_AT"}
		rows := make([][]string, 0, len(out.Data))
		for _, e := range out.Data {
			rows = append(rows, []string{
				e.FileName,
				e.Scope,
				formatBytes(e.Size),
				yesNo(e.Encrypted),
				fmt.Sprintf("v%d", e.FormatVersion),
				e.CreatedAt,
			})
		}
		f.Table(headers, rows)
		return nil
	},
}

var backupInspectCmd = &cobra.Command{
	Use:   "inspect <file>",
	Short: "Show the manifest of a backup bundle without decrypting the payload",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/backups/inspect?path=" + encodeQuery(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := cli.ReadJSON(resp, &raw); err != nil {
			return err
		}
		pretty, _ := json.MarshalIndent(raw, "", "  ")
		fmt.Println(string(pretty))
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
		if useKeyring && passphraseFile == "" && ws != "" {
			if kr, kerr := backup.DefaultKeyring(cmd.Context()); kerr == nil {
				if p, gerr := kr.GetPassphrase(cmd.Context(), ws); gerr == nil {
					passphrase = p
				}
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

var backupVerifyCmd = &cobra.Command{
	Use:   "verify <file>",
	Short: "Verify a bundle's SHA-256 checksum without restoring",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/backups/verify?path=" + encodeQuery(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Valid     bool   `json:"valid"`
			SizeBytes int64  `json:"size_bytes"`
			Error     string `json:"error"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if out.Valid {
			cli.PrintSuccess(fmt.Sprintf("VALID — %s (%s)", args[0], formatBytes(out.SizeBytes)))
			return nil
		}
		cli.PrintError(fmt.Sprintf("INVALID — %s: %s", args[0], out.Error))
		return fmt.Errorf("bundle verification failed")
	},
}

var backupUnlockCmd = &cobra.Command{
	Use:   "unlock",
	Short: "Force-release the advisory backup lock for this workspace",
	Long: `Release a stuck backup lock. Used when a previous backup
crashed or was killed mid-run and its defer-release did not fire.
Without --force this is interactive (stdin y/N prompt); in a
non-interactive session --force is mandatory.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("refusing to unlock without --force in a non-interactive session")
			}
			fmt.Fprint(os.Stderr, "Force-release the backup lock for this workspace? [y/N] ")
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line != "y" && line != "yes" {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
		}
		client := newAPIClient()
		resp, err := client.Delete("/api/v1/admin/backups/status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess("Backup lock released.")
		return nil
	},
}

var backupRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Apply retention policy — drop bundles over --keep-last or older than --keep-days",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		keepLast, _ := cmd.Flags().GetInt("keep-last")
		keepDays, _ := cmd.Flags().GetInt("keep-days")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if keepLast <= 0 && keepDays <= 0 {
			return fmt.Errorf("at least one of --keep-last or --keep-days must be positive")
		}
		body := map[string]any{
			"keep_last": keepLast,
			"keep_days": keepDays,
			"dry_run":   dryRun,
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/admin/backups/rotate", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Deleted []string `json:"deleted"`
			DryRun  bool     `json:"dry_run"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		verb := "Deleted"
		if out.DryRun {
			verb = "Would delete"
		}
		if len(out.Deleted) == 0 {
			fmt.Fprintln(os.Stderr, "No bundles matched the retention policy.")
			return nil
		}
		fmt.Fprintf(os.Stderr, "%s %d bundle(s):\n", verb, len(out.Deleted))
		for _, p := range out.Deleted {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		return nil
	},
}

var backupStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show active backup locks for the current workspace",
	Long: `Report whether a backup is currently in progress on this
workspace and when its advisory lock expires. Useful when
'backup create' returns 'another backup is already in progress' and
you need to know who acquired the lock (or wait for its TTL).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/backups/status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Held        bool   `json:"held"`
			AcquiredBy  string `json:"acquired_by,omitempty"`
			AcquiredAt  string `json:"acquired_at,omitempty"`
			ExpiresAt   string `json:"expires_at,omitempty"`
			WorkspaceID string `json:"workspace_id,omitempty"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		if !out.Held {
			fmt.Fprintln(os.Stderr, "No backup in progress on this workspace.")
			return nil
		}
		f := newFormatter()
		f.Table(
			[]string{"WORKSPACE", "ACQUIRED_BY", "ACQUIRED_AT", "EXPIRES_AT"},
			[][]string{{out.WorkspaceID, out.AcquiredBy, out.AcquiredAt, out.ExpiresAt}},
		)
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

func init() {
	backupCreateCmd.Flags().String("scope", "workspace", "Backup scope: workspace | crew")
	backupCreateCmd.Flags().String("crew", "", "Crew slug or ID (required for --scope=crew)")
	backupCreateCmd.Flags().Bool("no-encrypt", false, "Write a plaintext payload instead of AGE-encrypting it")
	backupCreateCmd.Flags().String("passphrase-file", "", "Read passphrase from file instead of prompting")
	backupCreateCmd.Flags().Bool("use-keyring", false, "Store and reuse the passphrase via the local backup keyring (~/.crewship/backup-keyring.enc)")
	backupCreateCmd.Flags().String("recipient", "", "AGE X25519 public key (age1…) for asymmetric encryption")
	backupCreateCmd.Flags().String("output", "", "Override output directory (default: ~/.crewship/backups on the server)")

	backupRestoreCmd.Flags().String("as-workspace", "", "Restore the workspace under a new slug")
	backupRestoreCmd.Flags().String("as-crew", "", "Restore the crew under a new slug (scope=crew only)")
	backupRestoreCmd.Flags().String("passphrase-file", "", "Read passphrase from file instead of prompting")
	backupRestoreCmd.Flags().Bool("use-keyring", false, "Read the passphrase from the local backup keyring before prompting")
	backupRestoreCmd.Flags().Bool("dry-run", false, "Verify compat, checksum and decryption without applying workspace/crew writes or docker changes (an audit row is still recorded)")

	backupRotateCmd.Flags().Int("keep-last", 0, "Keep only the N newest bundles (0 disables)")
	backupRotateCmd.Flags().Int("keep-days", 0, "Drop bundles older than N days (0 disables)")
	backupRotateCmd.Flags().Bool("dry-run", false, "List bundles that WOULD be deleted without touching disk")

	backupUnlockCmd.Flags().Bool("force", false, "Skip interactive confirmation (required in non-interactive sessions)")

	backupDeleteCmd.Flags().Bool("force", false, "Delete without interactive confirmation (required in non-interactive sessions)")

	backupCmd.AddCommand(backupCreateCmd)
	backupCmd.AddCommand(backupListCmd)
	backupCmd.AddCommand(backupInspectCmd)
	backupCmd.AddCommand(backupRestoreCmd)
	backupCmd.AddCommand(backupDeleteCmd)
	backupCmd.AddCommand(backupStatusCmd)
	backupCmd.AddCommand(backupVerifyCmd)
	backupCmd.AddCommand(backupUnlockCmd)
	backupCmd.AddCommand(backupRotateCmd)
}

// readPassphrase reads a passphrase from the given file, or prompts the
// user on stderr if the file path is empty. When confirm is true the
// user types the passphrase twice and mismatches return an error — this
// matches AGE's own CLI conventions and guards against typos on
// create-only flows. Restore accepts a single read.
func readPassphrase(file string, confirm bool) (string, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read passphrase file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	// Interactive prompt. We read from os.Stdin (not /dev/tty) since
	// the terminal detection below uses the same FD; scripts that want
	// to supply a passphrase without a TTY should pass --passphrase-file
	// or pipe a single line on stdin (handled immediately below).
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Non-interactive environment without --passphrase-file:
		// fall back to a single line from stdin.
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return "", errors.New("no passphrase on stdin")
		}
		return strings.TrimSpace(scanner.Text()), nil
	}
	fmt.Fprint(os.Stderr, "Passphrase: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	if !confirm {
		return string(first), nil
	}
	fmt.Fprint(os.Stderr, "Confirm passphrase: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read confirmation: %w", err)
	}
	if string(first) != string(second) {
		return "", errors.New("passphrases do not match")
	}
	if len(strings.TrimSpace(string(first))) == 0 {
		return "", errors.New("passphrase must not be empty")
	}
	return string(first), nil
}

// --- small formatting helpers kept local to avoid polluting other files.

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(int64(1)<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(int64(1)<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(int64(1)<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func truncateLong(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// encodeQuery delegates to net/url.QueryEscape. The previous hand-
// rolled escaper missed % and = — a bundle path containing either
// would have produced a malformed ?path= value. QueryEscape handles
// every reserved character the same way application/x-www-form-
// urlencoded expects.
func encodeQuery(s string) string {
	return url.QueryEscape(s)
}
