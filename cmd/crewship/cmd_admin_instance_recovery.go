//go:build !clionly

package main

// `crewship admin instance backups …` for whole-instance backup and recovery,
// and `crewship admin instance holds …`.
//
//	backups vault-keys                        GET  /api/v1/admin/instance/backups/vault-keys
//	backups recovery-kit on|off               PUT  /api/v1/admin/instance/backups/settings/recovery-kit
//	backups check <path>                      POST /api/v1/admin/instance/backups/bundles/check
//	backups restore-checks <path> --target t [--as-workspace s|--as-crew s]  POST /api/v1/admin/instance/backups/restore/checks
//	backups drills                            GET  /api/v1/admin/instance/backups/drills
//	holds list                                GET  /api/v1/admin/instance/holds
//	holds resume <key>                        POST /api/v1/admin/instance/holds/resume
//
// POST …/backups/drills is driven by `crewship backup drill --post`. Runs
// (`backups run`, `backups run-status`) live with the plans, in
// cmd_admin_instance_backup_plans.go.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type vaultKeyRow struct {
	Version   string `json:"version" yaml:"version"`
	Env       string `json:"env" yaml:"env"`
	Active    bool   `json:"active" yaml:"active"`
	Envelopes int    `json:"envelopes" yaml:"envelopes"`
	Present   bool   `json:"present" yaml:"present"`
}

type vaultKeysView struct {
	Versions    []vaultKeyRow `json:"versions" yaml:"versions"`
	RecoveryKit struct {
		Available bool `json:"available" yaml:"available"`
		Enabled   bool `json:"enabled" yaml:"enabled"`
	} `json:"recovery_kit" yaml:"recovery_kit"`
}

type bundleCheckView struct {
	OK         bool                       `json:"ok" yaml:"ok"`
	ProofLevel int                        `json:"proof_level" yaml:"proof_level"`
	Detail     string                     `json:"detail" yaml:"detail"`
	Problems   []string                   `json:"problems" yaml:"problems"`
	Absent     []instanceBackupIncomplete `json:"absent" yaml:"absent"`
	Entries    int                        `json:"entries" yaml:"entries"`
	Tables     int                        `json:"tables" yaml:"tables"`
	Files      int                        `json:"files" yaml:"files"`
}

type restoreChecksView struct {
	Space struct {
		OK        bool  `json:"ok" yaml:"ok"`
		NeedBytes int64 `json:"need_bytes" yaml:"need_bytes"`
		FreeBytes int64 `json:"free_bytes" yaml:"free_bytes"`
	} `json:"space" yaml:"space"`
	Format struct {
		OK        bool `json:"ok" yaml:"ok"`
		Version   int  `json:"version" yaml:"version"`
		Converter bool `json:"converter" yaml:"converter"`
	} `json:"format" yaml:"format"`
	Runtime struct {
		OK       bool     `json:"ok" yaml:"ok"`
		Detail   string   `json:"detail" yaml:"detail"`
		Warnings []string `json:"warnings" yaml:"warnings"`
	} `json:"runtime" yaml:"runtime"`
	Unsafe    []string `json:"unsafe" yaml:"unsafe"`
	Conflicts struct {
		OK     bool   `json:"ok" yaml:"ok"`
		Detail string `json:"detail" yaml:"detail"`
	} `json:"conflicts" yaml:"conflicts"`
	// One runtime check per complete container environment.
	Environments []struct {
		Crew         string `json:"crew" yaml:"crew"`
		Action       string `json:"action" yaml:"action"`
		Platform     string `json:"platform" yaml:"platform"`
		HostPlatform string `json:"host_platform" yaml:"host_platform"`
		MissingBlobs int    `json:"missing_blobs" yaml:"missing_blobs"`
		NeedBytes    int64  `json:"need_bytes" yaml:"need_bytes"`
		Detail       string `json:"detail" yaml:"detail"`
	} `json:"environments" yaml:"environments"`
}

type drillRow struct {
	ID          string `json:"id" yaml:"id"`
	Kind        string `json:"kind" yaml:"kind"`
	Actor       string `json:"actor" yaml:"actor"`
	BundlePath  string `json:"bundle_path" yaml:"bundle_path"`
	SourceScope string `json:"source_scope" yaml:"source_scope"`
	SourceName  string `json:"source_name" yaml:"source_name"`
	SourceDate  string `json:"source_date" yaml:"source_date"`
	Target      string `json:"target" yaml:"target"`
	Result      string `json:"result" yaml:"result"`
	Warnings    int    `json:"warnings" yaml:"warnings"`
	CreatedAt   string `json:"created_at" yaml:"created_at"`
}

type holdRow struct {
	Key       string `json:"key" yaml:"key"`
	Reason    string `json:"reason" yaml:"reason"`
	Count     int    `json:"count" yaml:"count"`
	Detail    string `json:"detail" yaml:"detail"`
	CreatedAt string `json:"created_at" yaml:"created_at"`
}

type holdResumed struct {
	Key     string `json:"key" yaml:"key"`
	Resumed bool   `json:"resumed" yaml:"resumed"`
}

// bundleKeyBody reads --identity / --passphrase-file into the request body.
// The key goes to the server for this one request and is never stored.
func bundleKeyBody(cmd *cobra.Command, body map[string]any) error {
	identity, _ := cmd.Flags().GetString("identity")
	passFile, _ := cmd.Flags().GetString("passphrase-file")
	if identity != "" && passFile != "" {
		return errors.New("supply only one of --identity or --passphrase-file")
	}
	if identity != "" {
		data, err := os.ReadFile(identity)
		if err != nil {
			return fmt.Errorf("read identity file: %w", err)
		}
		body["identity"] = string(data)
	}
	if passFile != "" {
		p, err := readPassphrase(passFile, false)
		if err != nil {
			return err
		}
		body["passphrase"] = p
	}
	return nil
}

var adminInstanceBackupsVaultKeysCmd = &cobra.Command{
	Use:   "vault-keys",
	Short: "Vault key versions on this server and how many sealed values use each",
	Long: `GET /api/v1/admin/instance/backups/vault-keys. Every key version the
database references or this server has configured, the environment variable
it is read from, how many sealed values (credentials, webhook secrets,
integration keys) use it, and whether the recovery kit is on. Counts only:
no key material is shown.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out vaultKeysView
		if err := getJSON(client, "/api/v1/admin/instance/backups/vault-keys", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "VERSION\tENV\tACTIVE\tSEALED VALUES\tKEY ON THIS SERVER")
			for _, v := range out.Versions {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", v.Version, v.Env, yesNo(v.Active), v.Envelopes, yesNo(v.Present))
			}
			_ = tw.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "Recovery kit: %s (every referenced key available: %s)\n",
				map[bool]string{true: "on", false: "off"}[out.RecoveryKit.Enabled], yesNo(out.RecoveryKit.Available))
		})
	},
}

var adminInstanceBackupsRecoveryKitCmd = &cobra.Command{
	Use:   "recovery-kit on|off",
	Short: "Put the vault keys in new instance backups, or stop",
	Long: `PUT /api/v1/admin/instance/backups/settings/recovery-kit. With the kit on,
every vault key version the database references rides inside new INSTANCE
bundles (never workspace or crew bundles), so a restore on a new server can
read the credentials. Whoever holds a private backup key can then read every
secret in the instance. Recorded in the instance audit log.`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"on", "off"},
	RunE: func(cmd *cobra.Command, args []string) error {
		var on bool
		switch args[0] {
		case "on":
			on = true
		case "off":
		default:
			return errors.New("say on or off")
		}
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out struct {
			Enabled bool `json:"enabled" yaml:"enabled"`
		}
		if err := putJSON(client, "/api/v1/admin/instance/backups/settings/recovery-kit", map[string]bool{"enabled": on}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if out.Enabled {
				fmt.Fprintln(cmd.OutOrStdout(), "Recovery kit on: new instance backups carry the vault keys. Whoever holds a private backup key can read every secret.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Recovery kit off: new instance backups carry no vault keys; a restore needs them from you.")
			}
		})
	},
}

var adminInstanceBackupsCheckCmd = &cobra.Command{
	Use:   "check <path>",
	Short: "Check a backup's contents: decrypt it, read every section, compare with its manifest",
	Long: `POST /api/v1/admin/instance/backups/bundles/check. Proof level 2: the server
decrypts the bundle with the key you give, reads every section to the end and
compares table row counts and file digests with the manifest. On success the
bundle is recorded as "contents checked". The key is sent for this check only
and never stored. Nothing is restored.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		body := map[string]any{"path": args[0]}
		if err := bundleKeyBody(cmd, body); err != nil {
			return err
		}
		timeout, _ := cmd.Flags().GetDuration("timeout")
		var out bundleCheckView
		if err := postJSON(client.WithTimeout(timeout), "/api/v1/admin/instance/backups/bundles/check", body, &out); err != nil {
			return err
		}
		if err := resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			verdict := "contents checked"
			if !out.OK {
				verdict = "does NOT match its manifest"
			}
			fmt.Fprintf(w, "%s: %s (proof level %d)\n%s\n", args[0], verdict, out.ProofLevel, out.Detail)
			for _, p := range out.Problems {
				fmt.Fprintf(w, "  problem: %s\n", p)
			}
			for _, a := range out.Absent {
				fmt.Fprintf(w, "  recorded missing: %s ×%d — %s\n", a.Kind, a.Count, a.Detail)
			}
		}); err != nil {
			return err
		}
		if !out.OK {
			return errors.New("the contents check failed")
		}
		return nil
	},
}

var adminInstanceBackupsRestoreChecksCmd = &cobra.Command{
	Use:   "restore-checks <path>",
	Short: "Checks a restore runs first: space, format, runtime, unsafe settings, conflicts",
	Long: `POST /api/v1/admin/instance/backups/restore/checks. Nothing changes. With a
key (--identity or --passphrase-file) the crews' configuration is also read
for host settings a restore must not switch back on by itself: Docker socket
mounts, privileged mode, host path binds. --target is empty_server, isolated,
replace, new_workspace or crew; new_workspace and crew are judged with the new
name they land under (--as-workspace / --as-crew), by the same rule the
restore applies.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		target, _ := cmd.Flags().GetString("target")
		body := map[string]any{"path": args[0], "target": target}
		if v, _ := cmd.Flags().GetString("as-workspace"); v != "" {
			body["as_workspace"] = v
		}
		if v, _ := cmd.Flags().GetString("as-crew"); v != "" {
			body["as_crew"] = v
		}
		if err := bundleKeyBody(cmd, body); err != nil {
			return err
		}
		timeout, _ := cmd.Flags().GetDuration("timeout")
		var out restoreChecksView
		if err := postJSON(client.WithTimeout(timeout), "/api/v1/admin/instance/backups/restore/checks", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			mark := func(ok bool) string {
				if ok {
					return "ok  "
				}
				return "FAIL"
			}
			fmt.Fprintf(w, "%s space      %d bytes needed, %d free\n", mark(out.Space.OK), out.Space.NeedBytes, out.Space.FreeBytes)
			how := "restorable directly"
			if out.Format.Converter {
				how = "through a converter"
			}
			fmt.Fprintf(w, "%s format     v%d, %s\n", mark(out.Format.OK), out.Format.Version, how)
			fmt.Fprintf(w, "%s runtime    %s\n", mark(out.Runtime.OK), out.Runtime.Detail)
			for _, x := range out.Runtime.Warnings {
				fmt.Fprintf(w, "     warning    %s\n", x)
			}
			for _, e := range out.Environments {
				fmt.Fprintf(w, "     environment %s: %s — %s\n", e.Crew, e.Action, e.Detail)
			}
			for _, x := range out.Unsafe {
				fmt.Fprintf(w, "     not carried over for safety: %s\n", x)
			}
			fmt.Fprintf(w, "%s conflicts  %s\n", mark(out.Conflicts.OK), out.Conflicts.Detail)
		})
	},
}

var adminInstanceBackupsDrillsCmd = &cobra.Command{
	Use:   "drills",
	Short: "List every recorded test restore (drill) and how it went",
	Long: `GET /api/v1/admin/instance/backups/drills. Drills are run offline with
'crewship backup drill --post' and recorded here with their result: ok,
partial (the WARN column counts what did not pass) or failed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out []drillRow
		if err := getJSON(client, "/api/v1/admin/instance/backups/drills", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tRESULT\tWARN\tSOURCE\tBY\tBUNDLE")
			for _, d := range out {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", d.CreatedAt, d.Result, d.Warnings, d.SourceName, d.Actor, d.BundlePath)
			}
			_ = tw.Flush()
		})
	},
}

var adminInstanceHoldsCmd = &cobra.Command{
	Use:   "holds",
	Short: "Automations an instance restore left held: list and resume",
	Long: `After 'crewship recover' the server boots with routines and schedules,
inbound webhooks and queued work held. Check the restored server, then resume
each one:

  crewship admin instance holds list
  crewship admin instance holds resume routines`,
}

var adminInstanceHoldsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List what is held",
	Long:  `GET /api/v1/admin/instance/holds.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out []holdRow
		if err := getJSON(client, "/api/v1/admin/instance/holds", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if len(out) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing is held.")
				return
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY\tSINCE\tDETAIL")
			for _, h := range out {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", h.Key, h.CreatedAt, h.Detail)
			}
			_ = tw.Flush()
		})
	},
}

var adminInstanceHoldsResumeCmd = &cobra.Command{
	Use:       "resume <key>",
	Short:     "Resume a held automation: routines, webhooks, queue or all",
	Long:      `POST /api/v1/admin/instance/holds/resume. Recorded in the instance audit log.`,
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"routines", "webhooks", "queue", "all"},
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out holdResumed
		if err := postJSON(client, "/api/v1/admin/instance/holds/resume", map[string]string{"key": strings.TrimSpace(args[0])}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Resumed %s.\n", out.Key)
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{adminInstanceBackupsCheckCmd, adminInstanceBackupsRestoreChecksCmd} {
		c.Flags().String("identity", "", "age identity file (AGE-SECRET-KEY-1…) that opens the bundle; sent for this request only")
		c.Flags().String("passphrase-file", "", "Read the passphrase that opens the bundle from a file")
		c.Flags().Duration("timeout", 30*time.Minute, "How long to wait for the server (a large bundle takes a while to read)")
	}
	adminInstanceBackupsRestoreChecksCmd.Flags().String("target", "empty_server", "empty_server | isolated | replace | new_workspace | crew")
	adminInstanceBackupsRestoreChecksCmd.Flags().String("as-workspace", "", "New workspace name (slug) a new_workspace target lands under")
	adminInstanceBackupsRestoreChecksCmd.Flags().String("as-crew", "", "New crew name (slug) a crew target lands under")

	adminInstanceBackupsCmd.AddCommand(adminInstanceBackupsVaultKeysCmd, adminInstanceBackupsRecoveryKitCmd, adminInstanceBackupsCheckCmd,
		adminInstanceBackupsRestoreChecksCmd, adminInstanceBackupsDrillsCmd)
	adminInstanceHoldsCmd.AddCommand(adminInstanceHoldsListCmd, adminInstanceHoldsResumeCmd)
	adminInstanceCmd.AddCommand(adminInstanceHoldsCmd)
}
