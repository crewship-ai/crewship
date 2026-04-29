package main

import (
	"bufio"
	"fmt"
	"os"
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
