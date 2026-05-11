package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

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

// backupMetricsCmd dumps the in-memory backup counters (created /
// failed totals, duration quantiles, lock-held). Process-lifetime so
// the numbers reset on a restart. Instance-OWNER gated server-side.
var backupMetricsCmd = &cobra.Command{
	Use:   "metrics",
	Short: "Show process-lifetime backup counters (instance owner only)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/backups/metrics")
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

// backupDownloadCmd streams a bundle by ID (or full path) from the
// server. Useful for pulling a remote bundle to a local workstation
// before restore. Honours --out for the destination file; without it
// the file is written into the cwd using the server-side basename.
var backupDownloadCmd = &cobra.Command{
	Use:   "download <bundle-path-or-id>",
	Short: "Stream a backup bundle to disk",
	Long: `Download the bundle bytes from the server's backup directory.
The argument is the bundle's full path as returned by 'backup list'.
Sensitive headers (no-store, no-cache) are applied server-side; the
client also disables cache. Writes to the basename in cwd unless --out
points elsewhere.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		path := args[0]
		resp, err := client.Get("/api/v1/admin/backups/download?path=" + encodeQuery(path))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		defer resp.Body.Close()

		dest, _ := cmd.Flags().GetString("out")
		if dest == "" {
			dest = filepath.Base(path)
		}
		// Refuse to clobber an existing file unless --force — the bundle
		// is the only authoritative copy of a workspace at a given
		// point in time, and silently overwriting one is destructive.
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("%s already exists; pass --force to overwrite", dest)
			}
		}
		f, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("create output: %w", err)
		}
		n, err := io.Copy(f, resp.Body)
		if err != nil {
			f.Close()
			// Leave nothing partial behind: a half-written bundle is
			// worse than no file — restore would silently truncate.
			_ = os.Remove(dest)
			return fmt.Errorf("write bundle: %w", err)
		}
		if cerr := f.Close(); cerr != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("close bundle: %w", cerr)
		}
		cli.PrintSuccess(fmt.Sprintf("Downloaded %s (%s)", dest, formatBytes(n)))
		return nil
	},
}

// backupSelfTestCmd runs the server-side canary round-trip (collect →
// destroy → restore → verify → cleanup) without touching the on-disk
// bundle layout. Quick way to validate the docker integration after
// upgrading the agent runtime image.
var backupSelfTestCmd = &cobra.Command{
	Use:   "self-test",
	Short: "Run the backup canary round-trip on a crew (admin)",
	Long: `Validates the backup pipeline end-to-end against a crew's
container, without producing a bundle on disk. Requires --crew. Server
returns the canary result with an "ok" boolean and per-stage timing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		crewArg, _ := cmd.Flags().GetString("crew")
		if crewArg == "" {
			return fmt.Errorf("--crew is required")
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewArg)
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/admin/backups/self-test", map[string]string{
			"crew_id": crewID,
		})
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

func init() {
	backupDownloadCmd.Flags().String("out", "", "Output file path (default: basename of source path)")
	backupDownloadCmd.Flags().Bool("force", false, "Overwrite the output file if it already exists")
	backupSelfTestCmd.Flags().String("crew", "", "Crew slug or ID to run the canary round-trip against (required)")

	backupCmd.AddCommand(backupMetricsCmd)
	backupCmd.AddCommand(backupDownloadCmd)
	backupCmd.AddCommand(backupSelfTestCmd)
}
