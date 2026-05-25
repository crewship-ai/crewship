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
	backupRestoreCmd.Flags().Bool("replace", false, "Wipe existing target rows matching the bundle's workspace (by id OR slug) BEFORE restore. Canonical disaster-recovery path: lands bundle data with original IDs after `dev.sh nuke` or a fresh-instance bootstrap re-took the slug under a new id. Mutually exclusive with --as-workspace / --as-crew.")

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
