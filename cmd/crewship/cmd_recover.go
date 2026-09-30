//go:build !clionly

package main

// Offline whole-instance recovery and test restores:
//
//	crewship recover --bundle <path> (--identity <file> | --passphrase-file <f>) --data-dir <dir> [--force]
//	crewship backup drill --bundle <path> (--identity <file> | --passphrase-file <f>) [--env-keys] [--post [url]]
//
// Neither needs a running server; recover never starts one. Only `--post`
// talks to a server, to record the drill's result.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/convert"
	"github.com/crewship-ai/crewship/internal/cli"
)

// bundleKeyFlags reads --identity / --passphrase-file for an offline command.
func bundleKeyFlags(cmd *cobra.Command) ([]age.Identity, string, error) {
	identityFile, _ := cmd.Flags().GetString("identity")
	passFile, _ := cmd.Flags().GetString("passphrase-file")
	switch {
	case identityFile != "" && passFile != "":
		return nil, "", errors.New("supply only one of --identity or --passphrase-file")
	case identityFile != "":
		ids, err := convert.ParseIdentityFile(identityFile)
		return ids, "", err
	case passFile != "":
		p, err := readPassphrase(passFile, false)
		return nil, p, err
	}
	return nil, "", errors.New("--identity or --passphrase-file is required: every instance bundle is encrypted")
}

var recoverCmd = &cobra.Command{
	Use:   "recover",
	Short: "Restore a whole instance from an instance backup into an empty data directory (offline)",
	Long: `Restore a whole Crewship instance from an instance bundle. Runs locally, with
no server running and none started: it writes a data directory a server can
then boot from.

What it does, in order:
  1. verifies the bundle's checksum and decrypts it with your key;
  2. refuses a non-empty --data-dir unless --force;
  3. writes the database and every file store (attachments, memory and its
     version history, page projects, chats, skills), checking each file
     against the bundle's own index;
  4. applies database migrations forward to this Crewship version;
  5. drops every sign-in session, clears locks and leases, and rotates the
     session signing key;
  6. writes the recovery kit's vault keys to <data-dir>/recovered-keys.env
     (mode 0600) — or says plainly that credentials stay unreadable when the
     bundle carries no kit;
  7. holds routines and schedules, inbound webhooks (503) and queued work, so
     the server boots with nothing firing until you resume each one with
     'crewship admin instance holds resume <key>'.

It prints a report (ok, partial or failed, and every gap) and stores it in the
restored database's restore history. Crew container files are kept under
<data-dir>/restore-staging/crews: there are no containers to land them in yet.

Examples:
  crewship recover --bundle crewship-instance-all-20260930T030000Z.tar.zst --identity ops.key --data-dir /var/lib/crewship
  crewship recover --bundle b.tar.zst --passphrase-file pass.txt --data-dir ./restored --force`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		bundle, _ := cmd.Flags().GetString("bundle")
		dataDir, _ := cmd.Flags().GetString("data-dir")
		force, _ := cmd.Flags().GetBool("force")
		if bundle == "" || dataDir == "" {
			return errors.New("--bundle and --data-dir are required")
		}
		ids, pass, err := bundleKeyFlags(cmd)
		if err != nil {
			return err
		}
		rep, err := backup.RecoverInstance(cmd.Context(), backup.RecoverOptions{
			BundlePath: bundle, Identities: ids, Passphrase: pass, DataDir: dataDir, Force: force,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			if rep != nil {
				_ = newFormatter().AutoHuman(rep, func() { printRecoverReport(cmd.OutOrStdout(), rep) })
			}
			return fmt.Errorf("recover failed: %w", err)
		}
		return newFormatter().AutoHuman(rep, func() { printRecoverReport(cmd.OutOrStdout(), rep) })
	},
}

func printRecoverReport(w io.Writer, r *backup.RecoverReport) {
	fmt.Fprintf(w, "Result: %s\n", r.Result)
	if r.Summary != "" {
		fmt.Fprintln(w, r.Summary)
	}
	fmt.Fprintf(w, "Data directory: %s\n", r.DataDir)
	if r.SourceHost != "" {
		fmt.Fprintf(w, "Backup of %s taken %s\n", r.SourceHost, r.BundleCreatedAt.Format("2006-01-02 15:04 MST"))
	}
	for _, s := range backup.InstanceStores {
		if st, ok := r.Stores[s]; ok {
			fmt.Fprintf(w, "  %-14s %d file(s), %d bytes → %s\n", s, st.Files, st.Bytes, st.Path)
		}
	}
	if r.KeysFile != "" {
		fmt.Fprintf(w, "Vault keys (%s) written to %s — give them to the server before it starts, then move them into your secret store and delete the file.\n",
			strings.Join(r.KitVersions, ", "), r.KeysFile)
	}
	if r.SecretsFile != "" {
		fmt.Fprintf(w, "New session signing key in %s: every session from the source is gone.\n", r.SecretsFile)
	}
	for _, h := range r.Holds {
		fmt.Fprintf(w, "Held: %-9s %s\n", h.Key, h.Detail)
	}
	for _, it := range r.Incomplete {
		fmt.Fprintf(w, "Missing: %s ×%d — %s\n", it.Kind, it.Count, it.Detail)
	}
	for _, x := range r.Warnings {
		fmt.Fprintf(w, "Warning: %s\n", x)
	}
	for _, x := range r.Notes {
		fmt.Fprintf(w, "Next: %s\n", x)
	}
}

var backupDrillCmd = &cobra.Command{
	Use:   "drill",
	Short: "Test-restore an instance backup in isolation and check it (offline)",
	Long: `Run a test restore of an instance bundle: the same restore 'crewship
recover' performs, into a throwaway directory that is deleted afterwards. No
server, scheduler, container or network client is started, so no message is
sent, no webhook called and no model run paid for.

Then it checks what a real recovery would be judged on:
  attachments_open    every attachment row's file exists with the right content
  memory_loads        every memory version's content is readable
  credentials_unlock  every sealed value decrypts with the recovery kit's keys
                      (or this machine's ENCRYPTION_KEY… with --env-keys)
  journal_verifies    every workspace's journal hash chain verifies
  routines_held       routines, webhooks and queued work are held

The result is ok, partial (what did not pass is listed) or failed. --post
records it on the server as the bundle's test restore (proof level 3) and in
its restore history; the bundle must be catalogued there under the same path
(--server-path when this machine sees it under another).

Examples:
  crewship backup drill --bundle crewship-instance-all-20260930T030000Z.tar.zst --identity ops.key
  crewship backup drill --bundle b.tar.zst --identity ops.key --post`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		bundle, _ := cmd.Flags().GetString("bundle")
		if bundle == "" {
			return errors.New("--bundle is required")
		}
		ids, pass, err := bundleKeyFlags(cmd)
		if err != nil {
			return err
		}
		envKeys, _ := cmd.Flags().GetBool("env-keys")
		post, _ := cmd.Flags().GetString("post")
		serverPath, _ := cmd.Flags().GetString("server-path")
		var client *cli.Client
		if post != "" {
			// Resolve the server before the drill runs, so a login problem
			// is not discovered after a long restore.
			if client, err = drillPostClient(post); err != nil {
				return err
			}
		}
		rep, err := backup.DrillInstance(cmd.Context(), backup.DrillOptions{
			BundlePath: bundle, Identities: ids, Passphrase: pass, UseEnvKeys: envKeys,
		})
		if err != nil {
			return err
		}
		var posted *drillPostResult
		if client != nil {
			if serverPath == "" {
				serverPath, _ = filepath.Abs(bundle)
			}
			body, err := json.Marshal(rep)
			if err != nil {
				return err
			}
			var res drillPostResult
			if err := postJSON(client, "/api/v1/admin/instance/backups/drills", map[string]any{
				"path": serverPath, "sha256": rep.SHA256, "result": rep.Result, "report": json.RawMessage(body),
			}, &res); err != nil {
				return fmt.Errorf("the drill ran (%s) but could not be recorded: %w", rep.Result, err)
			}
			posted = &res
		}
		if err := newFormatter().AutoHuman(rep, func() {
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Result: %s — %s\n", rep.Result, rep.Summary)
			for _, c := range rep.Checks {
				fmt.Fprintf(w, "  %-8s %-19s %s\n", c.Status, c.Name, c.Detail)
			}
			if rep.Restore != nil {
				for _, it := range rep.Restore.Incomplete {
					fmt.Fprintf(w, "  gap      %s ×%d — %s\n", it.Kind, it.Count, it.Detail)
				}
			}
			if posted != nil {
				fmt.Fprintf(w, "Recorded on the server: proof level %d, drill %s (report %s).\n", posted.ProofLevel, posted.DrillResult, posted.ReportID)
			}
		}); err != nil {
			return err
		}
		if rep.Result == backup.RestoreResultFailed {
			return errors.New("the test restore failed")
		}
		return nil
	},
}

type drillPostResult struct {
	Path        string `json:"path"`
	ProofLevel  int    `json:"proof_level"`
	DrillResult string `json:"drill_result"`
	DrillAt     string `json:"drill_at"`
	ReportID    string `json:"report_id"`
}

// drillPostClient returns the client --post records with. A URL other than
// the server this CLI is logged in to is refused: the stored token belongs to
// that server and is never sent anywhere else.
func drillPostClient(post string) (*cli.Client, error) {
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return nil, err
	}
	if post != "-" && strings.TrimRight(post, "/") != strings.TrimRight(client.BaseURL, "/") {
		return nil, fmt.Errorf("--post %s is not the server this CLI is logged in to (%s); log in there first (CREWSHIP_SERVER=%s crewship login) — the token is never sent to another server", post, client.BaseURL, post)
	}
	return client, nil
}

func init() {
	for _, c := range []*cobra.Command{recoverCmd, backupDrillCmd} {
		c.Flags().String("bundle", "", "Path to the instance bundle (required)")
		c.Flags().String("identity", "", "age identity file (AGE-SECRET-KEY-1…) that opens the bundle")
		c.Flags().String("passphrase-file", "", "Read the passphrase that opens the bundle from a file")
	}
	recoverCmd.Flags().String("data-dir", "", "Where to write the restored instance (required; must be empty unless --force)")
	recoverCmd.Flags().Bool("force", false, "Allow a non-empty data directory: its database is replaced and files the bundle carries are overwritten")
	backupDrillCmd.Flags().Bool("env-keys", false, "Without a recovery kit in the bundle, unlock credentials with this machine's ENCRYPTION_KEY / ENCRYPTION_KEY_V<N>")
	backupDrillCmd.Flags().String("post", "", "Record the result on the server this CLI is logged in to; --post=<url> also checks it is that server")
	backupDrillCmd.Flags().Lookup("post").NoOptDefVal = "-"
	backupDrillCmd.Flags().String("server-path", "", "With --post: the bundle's path as the server catalogued it (default: the absolute --bundle path)")
	rootCmd.AddCommand(recoverCmd)
	backupCmd.AddCommand(backupDrillCmd)
}
