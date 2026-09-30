//go:build !clionly

package main

// `crewship admin instance backups …` — Admin › Backups across workspaces.
//
//	bundles [--workspace a,b] [--scope s]   GET  /api/v1/admin/instance/backups/bundles
//	pin <path>                              POST /api/v1/admin/instance/backups/bundles/pin
//	unpin <path>                            POST /api/v1/admin/instance/backups/bundles/unpin
//	restores [--limit n]                    GET  /api/v1/admin/instance/backups/restores

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type instanceBackupIncomplete struct {
	Kind      string `json:"kind" yaml:"kind"`
	Detail    string `json:"detail" yaml:"detail"`
	Count     int    `json:"count" yaml:"count"`
	Workspace string `json:"workspace,omitempty" yaml:"workspace,omitempty"`
}

type instanceBackupBundleRow struct {
	ID             string                     `json:"id" yaml:"id"`
	Path           string                     `json:"path" yaml:"path"`
	FileName       string                     `json:"file_name" yaml:"file_name"`
	Scope          string                     `json:"scope" yaml:"scope"`
	ScopeLevel     string                     `json:"scope_level" yaml:"scope_level"`
	Kind           string                     `json:"kind" yaml:"kind"`
	Slug           string                     `json:"slug" yaml:"slug"`
	WorkspaceID    string                     `json:"workspace_id" yaml:"workspace_id"`
	WorkspaceName  string                     `json:"workspace_name" yaml:"workspace_name"`
	WorkspaceSlug  string                     `json:"workspace_slug" yaml:"workspace_slug"`
	CreatedAt      string                     `json:"created_at" yaml:"created_at"`
	CreatedBy      string                     `json:"created_by" yaml:"created_by"`
	SizeBytes      int64                      `json:"size_bytes" yaml:"size_bytes"`
	PayloadSHA256  string                     `json:"payload_sha256" yaml:"payload_sha256"`
	Encrypted      bool                       `json:"encrypted" yaml:"encrypted"`
	FormatVersion  int                        `json:"format_version" yaml:"format_version"`
	PlanID         string                     `json:"plan_id" yaml:"plan_id"`
	RunID          string                     `json:"run_id" yaml:"run_id"`
	Pinned         bool                       `json:"pinned" yaml:"pinned"`
	ProofLevel     int                        `json:"proof_level" yaml:"proof_level"`
	ProofCheckedAt *string                    `json:"proof_checked_at" yaml:"proof_checked_at"`
	DrillResult    string                     `json:"drill_result" yaml:"drill_result"`
	DrillAt        *string                    `json:"drill_at" yaml:"drill_at"`
	DrillReport    json.RawMessage            `json:"drill_report" yaml:"drill_report"`
	Incomplete     []instanceBackupIncomplete `json:"incomplete" yaml:"incomplete"`
}

type instanceBackupBundleList struct {
	Bundles []instanceBackupBundleRow `json:"bundles" yaml:"bundles"`
}

type instanceBackupPinResult struct {
	Path   string `json:"path" yaml:"path"`
	Pinned bool   `json:"pinned" yaml:"pinned"`
}

type instanceRestoreRow struct {
	ID          string          `json:"id" yaml:"id"`
	Kind        string          `json:"kind" yaml:"kind"`
	ActorUserID string          `json:"actor_user_id" yaml:"actor_user_id"`
	ActorEmail  string          `json:"actor_email" yaml:"actor_email"`
	BundlePath  string          `json:"bundle_path" yaml:"bundle_path"`
	Target      string          `json:"target" yaml:"target"`
	Result      string          `json:"result" yaml:"result"`
	Report      json.RawMessage `json:"report" yaml:"report"`
	CreatedAt   string          `json:"created_at" yaml:"created_at"`
}

type instanceRestoreList struct {
	Restores []instanceRestoreRow `json:"restores" yaml:"restores"`
}

var adminInstanceBackupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "Every workspace's backups: bundles, pins and restore reports",
	Long: `Admin › Backups from the terminal, across every workspace on the server —
including workspaces you are not a member of.

  crewship admin instance backups bundles
  crewship admin instance backups bundles --workspace coolify --scope workspace
  crewship admin instance backups pin <path>
  crewship admin instance backups restores --limit 20`,
}

// proofLabel names a proof level the way the console does.
func proofLabel(level int) string {
	switch level {
	case 3:
		return "test restore"
	case 2:
		return "contents checked"
	default:
		return "checksum"
	}
}

func incompleteLabel(items []instanceBackupIncomplete) string {
	if items == nil {
		return "not recorded"
	}
	if len(items) == 0 {
		return "complete"
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s×%d", it.Kind, it.Count))
	}
	return strings.Join(parts, ", ")
}

var adminInstanceBackupsBundlesCmd = &cobra.Command{
	Use:   "bundles",
	Short: "List every catalogued bundle with its proof level, pin and gaps",
	Long: `GET /api/v1/admin/instance/backups/bundles. One row per bundle across every
workspace: its proof level (checksum, contents checked, test restore), whether
it is pinned (no retention rule deletes it), and what it should hold and does
not (missing attachment files, memory blobs, crew containers). --workspace
narrows it to comma-separated slugs or ids, --scope to workspace or crew.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		q := url.Values{}
		if ws, _ := cmd.Flags().GetStringSlice("workspace"); len(ws) > 0 {
			q.Set("workspace", strings.Join(ws, ","))
		}
		if s, _ := cmd.Flags().GetString("scope"); s != "" {
			q.Set("scope", s)
		}
		path := "/api/v1/admin/instance/backups/bundles"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var out instanceBackupBundleList
		if err := getJSON(client, path, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "CREATED\tWORKSPACE\tSCOPE\tSIZE\tPROOF\tPINNED\tGAPS\tPATH")
			for _, b := range out.Bundles {
				ws := b.WorkspaceSlug
				if ws == "" {
					ws = b.WorkspaceID
				}
				pinned := "-"
				if b.Pinned {
					pinned = "pinned"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", b.CreatedAt, ws, b.Scope, b.SizeBytes,
					proofLabel(b.ProofLevel), pinned, incompleteLabel(b.Incomplete), b.Path)
			}
			_ = tw.Flush()
		})
	},
}

func runInstanceBackupPin(cmd *cobra.Command, path string, pin bool) error {
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return err
	}
	endpoint := "/api/v1/admin/instance/backups/bundles/unpin"
	if pin {
		endpoint = "/api/v1/admin/instance/backups/bundles/pin"
	}
	var out instanceBackupPinResult
	if err := postJSON(client, endpoint, map[string]string{"path": path}, &out); err != nil {
		return err
	}
	return resolvedFormatter(cmd).AutoHuman(out, func() {
		if out.Pinned {
			fmt.Fprintf(cmd.OutOrStdout(), "Pinned %s — no retention rule will delete it.\n", out.Path)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Unpinned %s — retention rules apply to it again.\n", out.Path)
		}
	})
}

var adminInstanceBackupsPinCmd = &cobra.Command{
	Use:   "pin <path>",
	Short: "Pin a bundle so no retention rule deletes it",
	Long: `POST /api/v1/admin/instance/backups/bundles/pin. The path is the bundle's
path as 'bundles' prints it. Recorded in the instance audit log.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInstanceBackupPin(cmd, args[0], true)
	},
}

var adminInstanceBackupsUnpinCmd = &cobra.Command{
	Use:   "unpin <path>",
	Short: "Unpin a bundle; retention rules apply to it again",
	Long: `POST /api/v1/admin/instance/backups/bundles/unpin. Recorded in the instance
audit log.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInstanceBackupPin(cmd, args[0], false)
	},
}

var adminInstanceBackupsRestoresCmd = &cobra.Command{
	Use:   "restores",
	Short: "List every restore, dry run and drill with its result",
	Long: `GET /api/v1/admin/instance/backups/restores. Newest first: who ran it, which
bundle, into what, and whether it was ok, partial (something was skipped,
missing or lowered — the report says what) or failed. --format json carries
each full report.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		var out instanceRestoreList
		if err := getJSON(client, fmt.Sprintf("/api/v1/admin/instance/backups/restores?limit=%d", limit), &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tKIND\tRESULT\tBY\tTARGET\tBUNDLE")
			for _, r := range out.Restores {
				by := r.ActorEmail
				if by == "" {
					by = r.ActorUserID
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.CreatedAt, r.Kind, r.Result, by, r.Target, r.BundlePath)
			}
			_ = tw.Flush()
		})
	},
}

func init() {
	b := adminInstanceBackupsBundlesCmd.Flags()
	b.StringSlice("workspace", nil, "Workspace slugs or ids, comma-separated (default: all)")
	b.String("scope", "", "workspace | crew")
	adminInstanceBackupsRestoresCmd.Flags().Int("limit", 100, "Rows to return (max 500)")

	adminInstanceBackupsCmd.AddCommand(adminInstanceBackupsBundlesCmd, adminInstanceBackupsPinCmd,
		adminInstanceBackupsUnpinCmd, adminInstanceBackupsRestoresCmd)
	adminInstanceCmd.AddCommand(adminInstanceBackupsCmd)
}
