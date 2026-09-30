//go:build !clionly

package main

// `crewship admin instance backups …` — settings, backup keys, off-site
// destinations, incidents and the recovery sheet (Track C2).
//
//	settings get                           GET    /api/v1/admin/instance/backups/settings
//	settings set [flags]                   PUT    /api/v1/admin/instance/backups/settings
//	recipients list                        GET    /api/v1/admin/instance/backups/recipients
//	recipients add --name --public-key     POST   /api/v1/admin/instance/backups/recipients
//	recipients remove <id>                 DELETE /api/v1/admin/instance/backups/recipients/{id}
//	destinations list                      GET    /api/v1/admin/instance/backups/destinations
//	destinations add [flags]               POST   /api/v1/admin/instance/backups/destinations
//	destinations test <id>                 POST   /api/v1/admin/instance/backups/destinations/{id}/test
//	destinations remove <id>               DELETE /api/v1/admin/instance/backups/destinations/{id}
//	incidents [--state --limit]            GET    /api/v1/admin/instance/backups/incidents
//	recovery-sheet [--out file]            GET    /api/v1/admin/instance/backups/recovery-sheet

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type backupLimitsView struct {
	Concurrency int `json:"concurrency" yaml:"concurrency"`
	CPUCores    int `json:"cpu_cores" yaml:"cpu_cores"`
	DiskMBps    int `json:"disk_mbps" yaml:"disk_mbps"`
	UploadMBps  int `json:"upload_mbps" yaml:"upload_mbps"`
}

type backupAlertEventsView struct {
	Failed     bool `json:"failed" yaml:"failed"`
	Incomplete bool `json:"incomplete" yaml:"incomplete"`
	Stale      bool `json:"stale" yaml:"stale"`
	Offsite    bool `json:"offsite" yaml:"offsite"`
	Drill      bool `json:"drill" yaml:"drill"`
}

type backupSettingsDestination struct {
	ID        string  `json:"id" yaml:"id"`
	Kind      string  `json:"kind" yaml:"kind"`
	Label     string  `json:"label" yaml:"label"`
	Path      *string `json:"path" yaml:"path"`
	UsedBytes *int64  `json:"used_bytes" yaml:"used_bytes"`
	Verified  bool    `json:"verified" yaml:"verified"`
	Available bool    `json:"available" yaml:"available"`
}

type backupSettingsView struct {
	Limits             backupLimitsView            `json:"limits" yaml:"limits"`
	HeartbeatURL       *string                     `json:"heartbeat_url" yaml:"heartbeat_url"`
	HeartbeatLastAt    *string                     `json:"heartbeat_last_at" yaml:"heartbeat_last_at"`
	HeartbeatLastOK    *bool                       `json:"heartbeat_last_ok" yaml:"heartbeat_last_ok"`
	HeartbeatLastError *string                     `json:"heartbeat_last_error" yaml:"heartbeat_last_error"`
	RecoveryKitEnabled bool                        `json:"recovery_kit_enabled" yaml:"recovery_kit_enabled"`
	Channels           []string                    `json:"channels" yaml:"channels"`
	Events             backupAlertEventsView       `json:"events" yaml:"events"`
	StaleAlertHours    int                         `json:"stale_alert_hours" yaml:"stale_alert_hours"`
	DrillReminder      string                      `json:"drill_reminder" yaml:"drill_reminder"`
	Destinations       []backupSettingsDestination `json:"destinations" yaml:"destinations"`
	InstanceAdmins     int                         `json:"instance_admins" yaml:"instance_admins"`
	LocalPath          *string                     `json:"local_path" yaml:"local_path"`
}

type backupRecipientRow struct {
	ID          string   `json:"id" yaml:"id"`
	Name        string   `json:"name" yaml:"name"`
	PublicKey   string   `json:"public_key" yaml:"public_key"`
	Holder      string   `json:"holder" yaml:"holder"`
	Fingerprint string   `json:"fingerprint" yaml:"fingerprint"`
	CreatedAt   string   `json:"created_at" yaml:"created_at"`
	UsedBy      []string `json:"used_by" yaml:"used_by"`
}

type backupDestinationRow struct {
	ID                  string   `json:"id" yaml:"id"`
	Name                string   `json:"name" yaml:"name"`
	Kind                string   `json:"kind" yaml:"kind"`
	Endpoint            string   `json:"endpoint" yaml:"endpoint"`
	Region              string   `json:"region" yaml:"region"`
	Bucket              string   `json:"bucket" yaml:"bucket"`
	Prefix              string   `json:"prefix" yaml:"prefix"`
	AccessKeyID         string   `json:"access_key_id" yaml:"access_key_id"`
	PathStyle           bool     `json:"path_style" yaml:"path_style"`
	AllowPrivateNetwork bool     `json:"allow_private_network" yaml:"allow_private_network"`
	LastTestAt          *string  `json:"last_test_at" yaml:"last_test_at"`
	LastTestError       *string  `json:"last_test_error" yaml:"last_test_error"`
	Copies              int      `json:"copies" yaml:"copies"`
	CopyBytes           int64    `json:"copy_bytes" yaml:"copy_bytes"`
	LastVerifiedAt      *string  `json:"last_verified_at" yaml:"last_verified_at"`
	UsedBy              []string `json:"used_by" yaml:"used_by"`
}

type backupDestinationTest struct {
	OK       bool    `json:"ok" yaml:"ok"`
	Error    *string `json:"error" yaml:"error"`
	TestedAt string  `json:"tested_at" yaml:"tested_at"`
}

type backupDestinationCreated struct {
	Destination backupDestinationRow   `json:"destination" yaml:"destination"`
	Test        *backupDestinationTest `json:"test" yaml:"test"`
	Warning     *string                `json:"warning" yaml:"warning"`
}

type backupIncidentRow struct {
	ID         string  `json:"id" yaml:"id"`
	PlanID     *string `json:"plan_id" yaml:"plan_id"`
	PlanName   *string `json:"plan_name" yaml:"plan_name"`
	Kind       string  `json:"kind" yaml:"kind"`
	State      string  `json:"state" yaml:"state"`
	Count      int     `json:"count" yaml:"count"`
	FirstAt    string  `json:"first_at" yaml:"first_at"`
	LastAt     string  `json:"last_at" yaml:"last_at"`
	ResolvedAt *string `json:"resolved_at" yaml:"resolved_at"`
	Message    string  `json:"message" yaml:"message"`
	RunID      *string `json:"run_id" yaml:"run_id"`
}

func mbpsLabel(n int) string {
	if n <= 0 {
		return "no limit"
	}
	return fmt.Sprintf("%d MB/s", n)
}

func printBackupSettings(cmd *cobra.Command, s backupSettingsView) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Limits:       %d run(s) at once · %d CPU core(s) · disk %s · upload %s\n",
		s.Limits.Concurrency, s.Limits.CPUCores, mbpsLabel(s.Limits.DiskMBps), mbpsLabel(s.Limits.UploadMBps))
	fmt.Fprintf(w, "Heartbeat:    %s", backupStrOr(s.HeartbeatURL, "not set"))
	if s.HeartbeatLastAt != nil {
		status := "ok"
		if s.HeartbeatLastOK != nil && !*s.HeartbeatLastOK {
			status = backupStrOr(s.HeartbeatLastError, "failed")
		}
		fmt.Fprintf(w, " (last ping %s: %s)", *s.HeartbeatLastAt, status)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Alerts:       failed %s · incomplete %s · stale after %d h %s · off-site %s · drill %s → %d instance admin(s)\n",
		onOff(s.Events.Failed), onOff(s.Events.Incomplete), s.StaleAlertHours, onOff(s.Events.Stale), onOff(s.Events.Offsite), onOff(s.Events.Drill), s.InstanceAdmins)
	if len(s.Channels) > 0 {
		fmt.Fprintf(w, "Channels:     %s\n", strings.Join(s.Channels, ", "))
	}
	fmt.Fprintf(w, "Drills:       remind %s\n", s.DrillReminder)
	fmt.Fprintf(w, "Recovery kit: %s\n", onOff(s.RecoveryKitEnabled))
	for _, d := range s.Destinations {
		switch {
		case d.Kind == "local":
			fmt.Fprintf(w, "Destination:  local · %s\n", backupStrOr(d.Path, "this server"))
		case !d.Available:
			fmt.Fprintf(w, "Destination:  %s · later\n", d.Label)
		default:
			state := "no checked upload yet"
			if d.Verified {
				state = "copies verified"
			}
			fmt.Fprintf(w, "Destination:  %s %s (%s) · %s\n", d.Kind, d.Label, d.ID, state)
		}
	}
}

var adminInstanceBackupsSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Instance backup settings: limits, heartbeat, alerts",
}

var adminInstanceBackupsSettingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the backup settings",
	Long: `GET /api/v1/admin/instance/backups/settings. Limits (runs at once, CPU
cores for compression, disk and upload MB/s), the heartbeat URL and its last
ping, which incidents reach the instance admins, the drill reminder, the
recovery kit, and where copies are kept.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out backupSettingsView
		if err := getJSON(client, instanceBackupsAPI+"/settings", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() { printBackupSettings(cmd, out) })
	},
}

var backupAlertEventNames = []string{"failed", "incomplete", "stale", "offsite", "drill"}

var adminInstanceBackupsSettingsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Change backup settings (only the flags you give)",
	Long: `PUT /api/v1/admin/instance/backups/settings. Only the flags you pass change;
the rest keep their value. --disk-mbps 0 and --upload-mbps 0 mean no limit.
--heartbeat-url "" clears the heartbeat. --alerts lists the incident kinds
that reach instance admins (failed, incomplete, stale, offsite, drill); the
others are still recorded. Recorded in the instance audit log.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		f := cmd.Flags()
		body := map[string]any{}
		limits := map[string]int{}
		for flag, key := range map[string]string{"concurrency": "concurrency", "cpu-cores": "cpu_cores", "disk-mbps": "disk_mbps", "upload-mbps": "upload_mbps"} {
			if f.Changed(flag) {
				v, _ := f.GetInt(flag)
				limits[key] = v
			}
		}
		if len(limits) > 0 {
			body["limits"] = limits
		}
		if f.Changed("heartbeat-url") {
			v, _ := f.GetString("heartbeat-url")
			if strings.TrimSpace(v) == "" {
				body["heartbeat_url"] = nil
			} else {
				body["heartbeat_url"] = v
			}
		}
		if f.Changed("alerts") {
			on, _ := f.GetStringSlice("alerts")
			events := map[string]bool{}
			for _, k := range backupAlertEventNames {
				events[k] = false
			}
			for _, k := range on {
				k = strings.TrimSpace(strings.ToLower(k))
				if k == "" || k == "none" {
					continue
				}
				if _, ok := events[k]; !ok {
					return fmt.Errorf("unknown alert %q (want %s)", k, strings.Join(backupAlertEventNames, ", "))
				}
				events[k] = true
			}
			body["events"] = events
		}
		if f.Changed("stale-hours") {
			v, _ := f.GetInt("stale-hours")
			body["stale_alert_hours"] = v
		}
		if f.Changed("drill-reminder") {
			v, _ := f.GetString("drill-reminder")
			body["drill_reminder"] = v
		}
		if f.Changed("channel") {
			v, _ := f.GetStringSlice("channel")
			body["channels"] = v
		}
		if len(body) == 0 {
			return errors.New("nothing to change: pass at least one flag (see --help)")
		}
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out backupSettingsView
		if err := putJSON(client, instanceBackupsAPI+"/settings", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() { printBackupSettings(cmd, out) })
	},
}

// ── Backup keys ─────────────────────────────────────────────────────────────

var adminInstanceBackupsRecipientsCmd = &cobra.Command{
	Use:     "recipients",
	Aliases: []string{"keys"},
	Short:   "Backup keys: the age public keys every backup is encrypted to",
}

var adminInstanceBackupsRecipientsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List backup keys and the plans that use them",
	Long:  `GET /api/v1/admin/instance/backups/recipients. Public halves only; the private halves never reach the server.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out struct {
			Data []backupRecipientRow `json:"data" yaml:"data"`
		}
		if err := getJSON(client, instanceBackupsAPI+"/recipients", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out.Data, func() {
			if len(out.Data) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No backup key yet. Every backup is encrypted: add one with `crewship admin instance backups recipients add`.")
				return
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tHELD BY\tFINGERPRINT\tUSED BY")
			for _, r := range out.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, backupDash(r.Holder), r.Fingerprint, backupDash(strings.Join(r.UsedBy, ", ")))
			}
			_ = tw.Flush()
		})
	},
}

func backupDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

var adminInstanceBackupsRecipientsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a backup key (an age public key, age1…)",
	Long: `POST /api/v1/admin/instance/backups/recipients. Give the PUBLIC key
(age1…): new backups of plans that name it are encrypted to it. The private
half (AGE-SECRET-KEY-1…) stays with --holder, off this server; a private key
is refused. Recorded in the instance audit log.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		name, _ := cmd.Flags().GetString("name")
		key, _ := cmd.Flags().GetString("public-key")
		holder, _ := cmd.Flags().GetString("holder")
		if strings.TrimSpace(name) == "" || strings.TrimSpace(key) == "" {
			return errors.New("--name and --public-key are required")
		}
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out backupRecipientRow
		if err := postJSON(client, instanceBackupsAPI+"/recipients", map[string]string{"name": name, "public_key": key, "holder": holder}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Backup key %s added (%s, %s). Name it in a plan's recipients to encrypt to it.\n", out.Name, out.ID, out.Fingerprint)
		})
	},
}

var adminInstanceBackupsRecipientsRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a backup key no plan uses",
	Long: `DELETE /api/v1/admin/instance/backups/recipients/{id}. Refused while a plan
still encrypts to the key: change the plan first. Backups already made stay
readable with the key's private half.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, instanceBackupsAPI+"/recipients/"+url.PathEscape(args[0])); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(map[string]any{"id": args[0], "removed": true}, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Backup key %s removed. Backups already made stay readable with its private half.\n", args[0])
		})
	},
}

// ── Off-site destinations ───────────────────────────────────────────────────

var adminInstanceBackupsDestinationsCmd = &cobra.Command{
	Use:   "destinations",
	Short: "Off-site destinations (S3-compatible storage) backups are copied to",
}

var adminInstanceBackupsDestinationsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List off-site destinations and their verified copies",
	Long:  `GET /api/v1/admin/instance/backups/destinations. The secret access key is never shown.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out struct {
			Data []backupDestinationRow `json:"data" yaml:"data"`
		}
		if err := getJSON(client, instanceBackupsAPI+"/destinations", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out.Data, func() {
			if len(out.Data) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No off-site destination: every backup sits on this server only.")
				return
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tENDPOINT\tBUCKET/PREFIX\tVERIFIED COPIES\tLAST TEST\tUSED BY")
			for _, d := range out.Data {
				test := "not tested"
				if d.LastTestAt != nil {
					test = "ok " + *d.LastTestAt
					if d.LastTestError != nil {
						test = "failed: " + *d.LastTestError
					}
				}
				loc := d.Bucket
				if d.Prefix != "" {
					loc += "/" + d.Prefix
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", d.ID, d.Name, d.Endpoint, loc, d.Copies, test, backupDash(strings.Join(d.UsedBy, ", ")))
			}
			_ = tw.Flush()
		})
	},
}

// readSecret reads the secret access key from a file ("-" is stdin).
func readSecret(path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return "", errors.New("the secret file is empty")
	}
	return s, nil
}

var adminInstanceBackupsDestinationsAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add an S3-compatible destination (R2, B2, Wasabi, MinIO, AWS)",
	Long: `POST /api/v1/admin/instance/backups/destinations. The server tests the
connection first (puts, reads back and deletes one small object) and stores
nothing when that fails. The secret access key is read from --secret-file
("-" for stdin), sealed with the server's vault key and never shown again.
--allow-private-network lets the endpoint be a LAN address (a MinIO next
door) and plain http; cloud metadata and link-local addresses stay blocked.
Name the destination's id in a plan's --destination to copy its backups
there. Recorded in the instance audit log.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		f := cmd.Flags()
		get := func(n string) string { v, _ := f.GetString(n); return v }
		secretFile := get("secret-file")
		if secretFile == "" {
			return errors.New("--secret-file is required (the secret access key; - reads stdin)")
		}
		secret, err := readSecret(secretFile)
		if err != nil {
			return err
		}
		pathStyle, _ := f.GetBool("path-style")
		private, _ := f.GetBool("allow-private-network")
		skip, _ := f.GetBool("skip-test")
		body := map[string]any{
			"name": get("name"), "kind": "s3", "endpoint": get("endpoint"), "region": get("region"), "bucket": get("bucket"),
			"prefix": get("prefix"), "access_key_id": get("access-key-id"), "secret_access_key": secret,
			"path_style": pathStyle, "allow_private_network": private, "skip_test": skip,
		}
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out backupDestinationCreated
		if err := postJSON(client, instanceBackupsAPI+"/destinations", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			if out.Warning != nil {
				fmt.Fprintf(w, "Warning: %s\n", *out.Warning)
			}
			tested := "stored without a connection test"
			if out.Test != nil && out.Test.OK {
				tested = "connection tested"
			}
			fmt.Fprintf(w, "Destination %s added (%s, %s). Add it to a plan: crewship admin instance backups plans update <plan> --destination local --destination %s\n",
				out.Destination.Name, out.Destination.ID, tested, out.Destination.ID)
		})
	},
}

var adminInstanceBackupsDestinationsTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Test a destination's connection and permissions again",
	Long:  `POST /api/v1/admin/instance/backups/destinations/{id}/test. Puts, reads back and deletes one small object. The outcome is stored with the destination.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out backupDestinationTest
		if err := postJSON(client, instanceBackupsAPI+"/destinations/"+url.PathEscape(args[0])+"/test", map[string]any{}, &out); err != nil {
			return err
		}
		if !out.OK {
			return fmt.Errorf("destination %s: connection failed: %s", args[0], backupStrOr(out.Error, "unknown error"))
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Destination %s: connection ok.\n", args[0])
		})
	},
}

var adminInstanceBackupsDestinationsRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a destination no plan copies to",
	Long: `DELETE /api/v1/admin/instance/backups/destinations/{id}. Refused while a plan
still copies there. The copies already uploaded stay in the bucket; only the
server's record of them goes.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, instanceBackupsAPI+"/destinations/"+url.PathEscape(args[0])); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(map[string]any{"id": args[0], "removed": true}, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Destination %s removed. Copies already uploaded stay in the bucket.\n", args[0])
		})
	},
}

// ── Incidents ───────────────────────────────────────────────────────────────

var adminInstanceBackupsIncidentsCmd = &cobra.Command{
	Use:   "incidents",
	Short: "Backup incidents: failed, incomplete, stale, off-site and drill",
	Long: `GET /api/v1/admin/instance/backups/incidents. One incident per plan and
kind, counted on each repeat and resolved by the next good run (or when the
condition clears). Open ones first.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		state, _ := cmd.Flags().GetString("state")
		limit, _ := cmd.Flags().GetInt("limit")
		q := url.Values{}
		if state != "" {
			q.Set("state", state)
		}
		if limit > 0 {
			q.Set("limit", fmt.Sprint(limit))
		}
		path := instanceBackupsAPI + "/incidents"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out struct {
			Data []backupIncidentRow `json:"data" yaml:"data"`
		}
		if err := getJSON(client, path, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out.Data, func() {
			if len(out.Data) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No backup incidents.")
				return
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "STATE\tKIND\tPLAN\tCOUNT\tLAST\tMESSAGE")
			for _, in := range out.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", in.State, in.Kind, backupStrOr(in.PlanName, "manual"), in.Count, in.LastAt, in.Message)
			}
			_ = tw.Flush()
		})
	},
}

// ── Recovery sheet ──────────────────────────────────────────────────────────

var adminInstanceBackupsRecoverySheetCmd = &cobra.Command{
	Use:   "recovery-sheet",
	Short: "Print the recovery sheet (Markdown) to keep off this server",
	Long: `GET /api/v1/admin/instance/backups/recovery-sheet. Where the bundles are
(this server and off-site), the backup keys' names and fingerprints (public
halves only), whether the vault keys ride in the recovery kit, the exact
crewship recover and drill commands, and who the instance admins are. It holds
no secret. --out writes it to a file.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		resp, err := client.Get(instanceBackupsAPI + "/recovery-sheet")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := cmd.Flags().GetString("out")
		if out == "" {
			_, err = io.Copy(cmd.OutOrStdout(), resp.Body)
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Recovery sheet written to %s\n", out)
		return nil
	},
}

func init() {
	s := adminInstanceBackupsSettingsSetCmd.Flags()
	s.Int("concurrency", 1, "Backup runs at once (1-16)")
	s.Int("cpu-cores", 2, "CPU cores for packing and compression")
	s.Int("disk-mbps", 0, "MB/s cap on writing staging and bundles (0 = no limit)")
	s.Int("upload-mbps", 0, "MB/s cap on off-site uploads (0 = no limit)")
	s.String("heartbeat-url", "", "Ping this public https URL after every good run (\"\" clears it)")
	s.StringSlice("alerts", nil, "Incident kinds that reach instance admins: failed,incomplete,stale,offsite,drill (none = no alerts)")
	s.Int("stale-hours", 36, "Raise a stale incident when a plan's newest good backup is older than this")
	s.String("drill-reminder", "monthly", "weekly | monthly | off")
	s.StringSlice("channel", nil, "Extra delivery routes, recorded with the settings; repeatable (replaces the list)")
	adminInstanceBackupsSettingsCmd.AddCommand(adminInstanceBackupsSettingsGetCmd, adminInstanceBackupsSettingsSetCmd)

	ra := adminInstanceBackupsRecipientsAddCmd.Flags()
	ra.String("name", "", "A name for the key, e.g. ops-2026 (required)")
	ra.String("public-key", "", "The age public key, age1… (required)")
	ra.String("holder", "", "Who holds the private half")
	adminInstanceBackupsRecipientsCmd.AddCommand(adminInstanceBackupsRecipientsListCmd, adminInstanceBackupsRecipientsAddCmd, adminInstanceBackupsRecipientsRemoveCmd)

	da := adminInstanceBackupsDestinationsAddCmd.Flags()
	da.String("name", "", "A name (default: bucket/prefix)")
	da.String("endpoint", "", "Service URL, e.g. https://<account>.r2.cloudflarestorage.com (required)")
	da.String("region", "", "Signing region (empty: us-east-1; R2 uses auto)")
	da.String("bucket", "", "Bucket (required)")
	da.String("prefix", "", "Key prefix inside the bucket, e.g. crewship/prod")
	da.String("access-key-id", "", "Access key id (required)")
	da.String("secret-file", "", "File holding the secret access key; - reads stdin (required)")
	da.Bool("path-style", false, "Address the bucket as endpoint/bucket (MinIO and most self-hosted stores)")
	da.Bool("allow-private-network", false, "Allow a LAN or loopback endpoint and plain http (metadata and link-local stay blocked)")
	da.Bool("skip-test", false, "Store without testing the connection first")
	adminInstanceBackupsDestinationsCmd.AddCommand(adminInstanceBackupsDestinationsListCmd, adminInstanceBackupsDestinationsAddCmd,
		adminInstanceBackupsDestinationsTestCmd, adminInstanceBackupsDestinationsRemoveCmd)

	adminInstanceBackupsIncidentsCmd.Flags().String("state", "", "open | resolved (default: both)")
	adminInstanceBackupsIncidentsCmd.Flags().Int("limit", 0, "Rows to return (default 100, max 500)")
	adminInstanceBackupsRecoverySheetCmd.Flags().String("out", "", "Write the sheet to this file instead of stdout")

	adminInstanceBackupsCmd.AddCommand(adminInstanceBackupsSettingsCmd, adminInstanceBackupsRecipientsCmd, adminInstanceBackupsDestinationsCmd,
		adminInstanceBackupsIncidentsCmd, adminInstanceBackupsRecoverySheetCmd)
}
