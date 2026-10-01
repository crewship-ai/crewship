//go:build !clionly

package main

// `crewship admin instance retention …` — Admin › Data retention.
//
//	get [--ws a,b]                          GET /api/v1/admin/instance/retention
//	set (--ws a,b | --all) --inbox-days …   PUT /api/v1/admin/instance/retention
//	defaults get                            GET /api/v1/admin/instance/retention/defaults
//	defaults set --inbox-days …             PUT /api/v1/admin/instance/retention/defaults
//
// `set` always asks the server first what it would change (dry_run) and how
// many rows the next sweep would then delete, prints that workspace by
// workspace, and writes only after a yes — shortening a window deletes data
// that no later change brings back (older backups still hold it).
//
// --all selects every existing workspace and nothing else. The defaults a new
// workspace starts with are `defaults set`, a separate operation with its own
// confirmation.

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// retentionWindowFlags maps each window to its flag and its column header, in
// the order the console lists them.
var retentionWindowFlags = []struct {
	key, flag, header string
}{
	{"routine_runs_days", "routine-runs-days", "RUNS"},
	{"approvals_days", "approvals-days", "APPROVALS"},
	{"audit_days", "audit-days", "AUDIT"},
	{"credential_audit_days", "credential-audit-days", "CRED AUDIT"},
	{"memory_versions_days", "memory-versions-days", "MEMORY"},
	{"page_panel_data_days", "page-panel-data-days", "PANELS"},
	{"inbox_days", "inbox-days", "INBOX"},
	{"chats_days", "chats-days", "CHATS"},
	{"keeper_decisions_days", "keeper-decisions-days", "KEEPER"},
}

type retentionRow struct {
	WorkspaceID   string          `json:"workspace_id" yaml:"workspace_id"`
	WorkspaceName string          `json:"workspace_name" yaml:"workspace_name"`
	WorkspaceSlug string          `json:"workspace_slug" yaml:"workspace_slug"`
	Windows       map[string]*int `json:"windows" yaml:"windows"`
}

type retentionList struct {
	Workspaces         []retentionRow  `json:"workspaces" yaml:"workspaces"`
	Defaults           map[string]*int `json:"defaults" yaml:"defaults"`
	DefaultsConfigured []string        `json:"defaults_configured" yaml:"defaults_configured"`
	Keys               []struct {
		Key            string `json:"key" yaml:"key"`
		Label          string `json:"label" yaml:"label"`
		DefaultDays    *int   `json:"default_days" yaml:"default_days"`
		ForeverAllowed bool   `json:"forever_allowed" yaml:"forever_allowed"`
		MinDays        int    `json:"min_days" yaml:"min_days"`
		MaxDays        int    `json:"max_days" yaml:"max_days"`
		Detail         string `json:"detail" yaml:"detail"`
	} `json:"keys" yaml:"keys"`
	Housekeeping []struct {
		Key    string `json:"key" yaml:"key"`
		Label  string `json:"label" yaml:"label"`
		Value  string `json:"value" yaml:"value"`
		Detail string `json:"detail" yaml:"detail"`
	} `json:"housekeeping" yaml:"housekeeping"`
}

type retentionChange struct {
	Key                   string `json:"key" yaml:"key"`
	From                  *int   `json:"from" yaml:"from"`
	To                    *int   `json:"to" yaml:"to"`
	RowsAffectedNextSweep int64  `json:"rows_affected_next_sweep" yaml:"rows_affected_next_sweep"`
}

type retentionPutResult struct {
	Applied               bool   `json:"applied" yaml:"applied"`
	DryRun                bool   `json:"dry_run" yaml:"dry_run"`
	Changed               int    `json:"changed" yaml:"changed"`
	RowsAffectedNextSweep int64  `json:"rows_affected_next_sweep" yaml:"rows_affected_next_sweep"`
	PreviewID             string `json:"preview_id" yaml:"preview_id"`
	Workspaces            []struct {
		WorkspaceID   string            `json:"workspace_id" yaml:"workspace_id"`
		WorkspaceName string            `json:"workspace_name" yaml:"workspace_name"`
		WorkspaceSlug string            `json:"workspace_slug" yaml:"workspace_slug"`
		Changes       []retentionChange `json:"changes" yaml:"changes"`
	} `json:"workspaces" yaml:"workspaces"`
}

func retentionDays(v *int) string {
	if v == nil {
		return "forever"
	}
	return fmt.Sprintf("%dd", *v)
}

var adminInstanceRetentionCmd = &cobra.Command{
	Use:   "retention",
	Short: "How long every workspace keeps each kind of data",
	Long: `Admin › Data retention from the terminal, across every workspace on the
server — including workspaces you are not a member of. Windows are in days;
"forever" keeps everything. --all means every workspace that exists now; what
a workspace created later starts with is set apart, with 'defaults set'.

Inbox items, chats and Keeper decisions are kept forever until you set a
window. Shortening a window deletes at the next daily sweep what falls
outside it; backups taken before still hold those rows.

  crewship admin instance retention get
  crewship admin instance retention set --ws coolify --inbox-days 90 --chats-days forever
  crewship admin instance retention set --all --keeper-decisions-days 365 --dry-run
  crewship admin instance retention defaults set --inbox-days 90`,
}

var adminInstanceRetentionGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show every workspace's retention windows",
	Long: `GET /api/v1/admin/instance/retention. One row per workspace with each
window, then the defaults a new workspace starts with and the fixed limits
the server applies on its own. --ws narrows it to some workspaces
(comma-separated slugs or ids).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		path := "/api/v1/admin/instance/retention"
		if ws, _ := cmd.Flags().GetStringSlice("ws"); len(ws) > 0 {
			q := url.Values{}
			for _, w := range ws {
				q.Add("ws", w)
			}
			path += "?" + q.Encode()
		}
		var out retentionList
		if err := getJSON(client, path, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			head := []string{"WORKSPACE"}
			for _, f := range retentionWindowFlags {
				head = append(head, f.header)
			}
			fmt.Fprintln(tw, strings.Join(head, "\t"))
			row := func(name string, win map[string]*int) {
				cells := []string{name}
				for _, f := range retentionWindowFlags {
					cells = append(cells, retentionDays(win[f.key]))
				}
				fmt.Fprintln(tw, strings.Join(cells, "\t"))
			}
			for _, r := range out.Workspaces {
				row(r.WorkspaceSlug, r.Windows)
			}
			row("(new workspaces)", out.Defaults)
			_ = tw.Flush()
			if len(out.Housekeeping) > 0 {
				fmt.Fprintln(w, "\nFixed instance limits:")
				for _, h := range out.Housekeeping {
					fmt.Fprintf(w, "  %-40s %s\n", h.Label, h.Value)
				}
			}
		})
	},
}

var adminInstanceRetentionSetCmd = &cobra.Command{
	Use:   "set (--ws <slug>[,<slug>…] | --all) --<window>-days <N|forever> …",
	Short: "Set retention windows for one, several or all workspaces",
	Long: `PUT /api/v1/admin/instance/retention. Only the windows you pass change;
each takes a number of days (1–3650) or "forever". Routine runs, memory
versions and page panel data always age out and cannot be forever.

It first shows every workspace that would change and how many rows the next
sweep would then delete, and asks; --yes skips the question, --dry-run only
shows. The save is all or nothing. --all selects every existing workspace; it
does not change what a workspace created later starts with — that is
'retention defaults set'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		f := cmd.Flags()
		all, _ := f.GetBool("all")
		wsList, _ := f.GetStringSlice("ws")
		dry, _ := f.GetBool("dry-run")
		if all == (len(wsList) > 0) {
			return cli.WithExitCode(errors.New("choose --ws <slug>[,<slug>…] or --all"), cli.ExitValidation)
		}
		windows, err := retentionWindowsFromFlags(cmd)
		if err != nil {
			return err
		}
		if len(windows) == 0 {
			return cli.WithExitCode(errors.New("nothing to change — pass at least one window, e.g. --inbox-days 90"), cli.ExitValidation)
		}
		body := map[string]any{"windows": windows, "workspace_ids": nil, "dry_run": true}
		if !all {
			body["workspace_ids"] = wsList
		}
		var preview retentionPutResult
		if err := putJSON(client, "/api/v1/admin/instance/retention", body, &preview); err != nil {
			return err
		}
		if dry {
			return resolvedFormatter(cmd).AutoHuman(preview, func() { printRetentionChanges(cmd, preview, "would change") })
		}
		printRetentionChanges(cmd, preview, "will change")
		if preview.Changed == 0 {
			cli.PrintSuccess("Nothing to change: every selected workspace already has these windows.")
			return nil
		}
		question := fmt.Sprintf("Change the retention of %d workspace(s)?", preview.Changed)
		if preview.RowsAffectedNextSweep > 0 {
			question = fmt.Sprintf("Change the retention of %d workspace(s)? The next sweep deletes %d row(s).", preview.Changed, preview.RowsAffectedNextSweep)
		}
		if err := confirmAction(cmd, question); err != nil {
			return cli.WithExitCode(err, cli.ExitValidation)
		}
		body["dry_run"] = false
		// Save exactly what was shown: a workspace created or a window changed
		// since the preview makes the server answer 409.
		body["expect_preview"] = preview.PreviewID
		var out retentionPutResult
		if err := putJSON(client, "/api/v1/admin/instance/retention", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("Saved in %d workspace(s). New workspaces are unaffected; see 'retention defaults set'.", out.Changed))
		})
	},
}

// retentionWindowsFromFlags reads the --<window>-days flags that were passed:
// a number of days, or "forever" (sent as null). Bounds are the server's to
// enforce; this only refuses what is not a number.
func retentionWindowsFromFlags(cmd *cobra.Command) (map[string]any, error) {
	out := map[string]any{}
	for _, wf := range retentionWindowFlags {
		if !cmd.Flags().Changed(wf.flag) {
			continue
		}
		v, _ := cmd.Flags().GetString(wf.flag)
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "forever" {
			out[wf.key] = nil
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil {
			return nil, cli.WithExitCode(fmt.Errorf("--%s wants a number of days or forever, not %q", wf.flag, v), cli.ExitValidation)
		}
		out[wf.key] = n
	}
	return out, nil
}

func printRetentionChanges(cmd *cobra.Command, r retentionPutResult, verb string) {
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "%d workspace(s) %s:\n", r.Changed, verb)
	for _, ws := range r.Workspaces {
		if len(ws.Changes) == 0 {
			fmt.Fprintf(w, "  %-24s no change\n", ws.WorkspaceSlug)
			continue
		}
		parts := make([]string, 0, len(ws.Changes))
		for _, c := range ws.Changes {
			p := fmt.Sprintf("%s %s → %s", c.Key, retentionDays(c.From), retentionDays(c.To))
			if c.RowsAffectedNextSweep > 0 {
				p += fmt.Sprintf(" (%d row(s) at the next sweep)", c.RowsAffectedNextSweep)
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(w, "  %-24s %s\n", ws.WorkspaceSlug, strings.Join(parts, ", "))
	}
	if r.RowsAffectedNextSweep > 0 {
		fmt.Fprintf(w, "  %d row(s) go at the next sweep; backups taken before still hold them.\n", r.RowsAffectedNextSweep)
	}
}

type retentionDefaults struct {
	Defaults   map[string]*int `json:"defaults" yaml:"defaults"`
	Configured []string        `json:"configured" yaml:"configured"`
}

type retentionDefaultsPutResult struct {
	Applied         bool              `json:"applied" yaml:"applied"`
	DryRun          bool              `json:"dry_run" yaml:"dry_run"`
	AffectsExisting bool              `json:"affects_existing" yaml:"affects_existing"`
	Changes         []retentionChange `json:"changes" yaml:"changes"`
	Defaults        map[string]*int   `json:"defaults" yaml:"defaults"`
	PreviewID       string            `json:"preview_id" yaml:"preview_id"`
}

var adminInstanceRetentionDefaultsCmd = &cobra.Command{
	Use:   "defaults",
	Short: "The retention windows a workspace created later starts with",
	Long: `What a new workspace keeps, window by window. Changing these touches no
existing workspace and deletes nothing; use 'retention set' for those.`,
}

var adminInstanceRetentionDefaultsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the retention windows new workspaces start with",
	Long: `GET /api/v1/admin/instance/retention/defaults. Every window a workspace
created now starts with. SOURCE says whether an administrator set it or it is
the product default.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var out retentionDefaults
		if err := getJSON(client, "/api/v1/admin/instance/retention/defaults", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			set := map[string]bool{}
			for _, k := range out.Configured {
				set[k] = true
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WINDOW\tNEW WORKSPACES KEEP\tSOURCE")
			for _, f := range retentionWindowFlags {
				src := "product default"
				if set[f.key] {
					src = "set by an admin"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", f.key, retentionDays(out.Defaults[f.key]), src)
			}
			_ = tw.Flush()
		})
	},
}

var adminInstanceRetentionDefaultsSetCmd = &cobra.Command{
	Use:   "set --<window>-days <N|forever> …",
	Short: "Set the retention windows new workspaces start with",
	Long: `PUT /api/v1/admin/instance/retention/defaults. Only the windows you pass
change; each takes a number of days (1–3650) or "forever". No existing
workspace changes and nothing is deleted: this is only what a workspace
created later starts with. It shows the change first and asks; --yes skips the
question, --dry-run only shows. The confirmation saves exactly what the
preview showed: if another admin changed a default in between, the server
refuses and nothing is written.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		dry, _ := cmd.Flags().GetBool("dry-run")
		windows, err := retentionWindowsFromFlags(cmd)
		if err != nil {
			return err
		}
		if len(windows) == 0 {
			return cli.WithExitCode(errors.New("nothing to change — pass at least one window, e.g. --inbox-days 90"), cli.ExitValidation)
		}
		body := map[string]any{"windows": windows, "dry_run": true}
		var preview retentionDefaultsPutResult
		if err := putJSON(client, "/api/v1/admin/instance/retention/defaults", body, &preview); err != nil {
			return err
		}
		if dry {
			return resolvedFormatter(cmd).AutoHuman(preview, func() { printRetentionDefaultChanges(cmd, preview, "would change") })
		}
		printRetentionDefaultChanges(cmd, preview, "will change")
		if len(preview.Changes) == 0 {
			cli.PrintSuccess("Nothing to change: new workspaces already start with these windows.")
			return nil
		}
		if err := confirmAction(cmd, fmt.Sprintf("Change %d default(s) for new workspaces?", len(preview.Changes))); err != nil {
			return cli.WithExitCode(err, cli.ExitValidation)
		}
		body["dry_run"] = false
		// Save exactly what was shown: a default another admin changed since
		// the preview makes the server answer 409 and nothing is written.
		body["expect_preview"] = preview.PreviewID
		var out retentionDefaultsPutResult
		if err := putJSON(client, "/api/v1/admin/instance/retention/defaults", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("New workspaces will start with these windows (%d changed). Existing workspaces are unchanged.", len(out.Changes)))
		})
	},
}

func printRetentionDefaultChanges(cmd *cobra.Command, r retentionDefaultsPutResult, verb string) {
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "Defaults for new workspaces: %d %s\n", len(r.Changes), verb)
	for _, c := range r.Changes {
		fmt.Fprintf(w, "  %-24s %s → %s\n", c.Key, retentionDays(c.From), retentionDays(c.To))
	}
	fmt.Fprintln(w, "  Existing workspaces are not touched and nothing is deleted.")
}

// addRetentionWindowFlags registers one --<window>-days flag per window.
func addRetentionWindowFlags(cmd *cobra.Command) {
	s := cmd.Flags()
	s.String("routine-runs-days", "", "Finished routine runs: days (always keeps the last 10 per routine)")
	s.String("approvals-days", "", "Decided approvals: days or forever")
	s.String("audit-days", "", "Workspace audit log: days or forever")
	s.String("credential-audit-days", "", "Credential reads: days or forever")
	s.String("memory-versions-days", "", "Memory version history: days")
	s.String("page-panel-data-days", "", "Page panel values: days")
	s.String("inbox-days", "", "Resolved and read, non-blocking inbox items: days or forever")
	s.String("chats-days", "", "Idle chats without open work: days or forever")
	s.String("keeper-decisions-days", "", "Decided Keeper requests: days or forever")
}

func init() {
	adminInstanceRetentionGetCmd.Flags().StringSlice("ws", nil, "Workspace slugs or ids, comma-separated (default: all)")

	s := adminInstanceRetentionSetCmd.Flags()
	s.StringSlice("ws", nil, "Workspace slugs or ids, comma-separated")
	s.Bool("all", false, "Every existing workspace (new workspaces are unaffected)")
	s.Bool("dry-run", false, "Show what would change and what the next sweep would delete; write nothing")
	s.BoolP("yes", "y", false, "Skip the confirmation")
	addRetentionWindowFlags(adminInstanceRetentionSetCmd)

	d := adminInstanceRetentionDefaultsSetCmd.Flags()
	d.Bool("dry-run", false, "Show what would change; write nothing")
	d.BoolP("yes", "y", false, "Skip the confirmation")
	addRetentionWindowFlags(adminInstanceRetentionDefaultsSetCmd)
	adminInstanceRetentionDefaultsCmd.AddCommand(adminInstanceRetentionDefaultsGetCmd, adminInstanceRetentionDefaultsSetCmd)

	adminInstanceRetentionCmd.AddCommand(adminInstanceRetentionGetCmd, adminInstanceRetentionSetCmd, adminInstanceRetentionDefaultsCmd)
	adminInstanceCmd.AddCommand(adminInstanceRetentionCmd)
}
