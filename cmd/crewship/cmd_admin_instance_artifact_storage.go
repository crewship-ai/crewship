//go:build !clionly

package main

// `crewship admin instance artifact-storage` — the aggregate artifact storage
// limits and the counter recompute (GET/PUT /api/v1/admin/instance/artifact-storage,
// POST /api/v1/admin/instance/artifact-storage/recompute).

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

const artifactStoragePath = "/api/v1/admin/instance/artifact-storage"

type adminArtifactStorage struct {
	WorkspaceLimitBytes int64  `json:"workspace_limit_bytes" yaml:"workspace_limit_bytes"`
	InstanceLimitBytes  int64  `json:"instance_limit_bytes" yaml:"instance_limit_bytes"`
	UsedBytes           int64  `json:"used_bytes" yaml:"used_bytes"`
	ReservedBytes       int64  `json:"reserved_bytes" yaml:"reserved_bytes"`
	Level               string `json:"level" yaml:"level"`
	Workspaces          int    `json:"workspaces" yaml:"workspaces"`
	Retention           string `json:"retention" yaml:"retention"`
}

type adminArtifactStorageDrift struct {
	UsedBefore     int64 `json:"used_bytes_before" yaml:"used_bytes_before"`
	UsedAfter      int64 `json:"used_bytes_after" yaml:"used_bytes_after"`
	ReservedBefore int64 `json:"reserved_bytes_before" yaml:"reserved_bytes_before"`
	ReservedAfter  int64 `json:"reserved_bytes_after" yaml:"reserved_bytes_after"`
}

type adminArtifactStorageWorkspaceDrift struct {
	WorkspaceID    string `json:"workspace_id" yaml:"workspace_id"`
	UsedBefore     int64  `json:"used_bytes_before" yaml:"used_bytes_before"`
	UsedAfter      int64  `json:"used_bytes_after" yaml:"used_bytes_after"`
	ReservedBefore int64  `json:"reserved_bytes_before" yaml:"reserved_bytes_before"`
	ReservedAfter  int64  `json:"reserved_bytes_after" yaml:"reserved_bytes_after"`
}

type adminArtifactStorageRecompute struct {
	DryRun            bool                                 `json:"dry_run" yaml:"dry_run"`
	Changed           bool                                 `json:"changed" yaml:"changed"`
	Instance          adminArtifactStorageDrift            `json:"instance" yaml:"instance"`
	WorkspacesChecked int                                  `json:"workspaces_checked" yaml:"workspaces_checked"`
	WorkspacesDrifted int                                  `json:"workspaces_drifted" yaml:"workspaces_drifted"`
	Workspaces        []adminArtifactStorageWorkspaceDrift `json:"workspaces" yaml:"workspaces"`
	DataDeleted       bool                                 `json:"data_deleted" yaml:"data_deleted"`
}

// parseStorageBytes reads a byte count: plain bytes, or a number with a binary
// suffix (KiB, MiB, GiB, TiB; K, M, G, T mean the same).
func parseStorageBytes(flag, raw string) (int64, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10}, {"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > (1<<62)/mult {
		return 0, fmt.Errorf("--%s %q: want a positive size such as 64GiB, 512MiB or a byte count", flag, raw)
	}
	return n * mult, nil
}

var adminInstanceArtifactStorageCmd = &cobra.Command{
	Use:   "artifact-storage",
	Short: "Aggregate artifact storage limits and counters",
	Long: `The aggregate artifact storage budget of this server, for instance
administrators: the per-workspace and the instance limit, the instance's used
and in-flight bytes, and the coarse level.

Defaults are 32 GiB per workspace and 64 GiB per instance. When a limit is
reached, writes are refused with 413 artifact_storage_quota; nothing is ever
deleted to make room, and lowering a limit below current usage only refuses
growth.

  crewship admin instance artifact-storage                      show
  crewship admin instance artifact-storage set --workspace-limit 16GiB --instance-limit 48GiB
  crewship admin instance artifact-storage recompute --dry-run  preview drift
  crewship admin instance artifact-storage recompute            repair the counters`,
	Args: cobra.NoArgs,
	RunE: showAdminArtifactStorage,
}

func showAdminArtifactStorage(cmd *cobra.Command, _ []string) error {
	client, err := requireAuthInstance()
	if err != nil {
		return err
	}
	var out adminArtifactStorage
	if err := getJSON(client, artifactStoragePath, &out); err != nil {
		return err
	}
	return printAdminArtifactStorage(cmd, out)
}

func printAdminArtifactStorage(cmd *cobra.Command, out adminArtifactStorage) error {
	return resolvedFormatter(cmd).AutoHuman(out, func() {
		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "Workspace limit:  %s\n", formatBytes(out.WorkspaceLimitBytes))
		fmt.Fprintf(w, "Instance limit:   %s\n", formatBytes(out.InstanceLimitBytes))
		fmt.Fprintf(w, "Instance used:    %s (+ %s in flight), level %s, %d workspace(s) with artifacts\n", formatBytes(out.UsedBytes), formatBytes(out.ReservedBytes), out.Level, out.Workspaces)
		fmt.Fprintln(w, "History is never deleted automatically; a full budget refuses writes with 413 artifact_storage_quota.")
	})
}

var adminInstanceArtifactStorageGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the limits and instance counters",
	Args:  cobra.NoArgs,
	RunE:  showAdminArtifactStorage,
}

var adminInstanceArtifactStorageSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the workspace and/or instance limit",
	Long: `PUT /api/v1/admin/instance/artifact-storage. Sizes are bytes or carry a
binary suffix (512MiB, 16GiB, 1TiB). A limit you leave out keeps its value.
Between 1 MiB and 1 PiB. No data is removed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		body := map[string]any{}
		for flag, key := range map[string]string{"workspace-limit": "workspace_limit_bytes", "instance-limit": "instance_limit_bytes"} {
			if !cmd.Flags().Changed(flag) {
				continue
			}
			raw, _ := cmd.Flags().GetString(flag)
			n, err := parseStorageBytes(flag, raw)
			if err != nil {
				return cli.WithExitCode(err, cli.ExitValidation)
			}
			body[key] = n
		}
		if len(body) == 0 {
			return cli.WithExitCode(fmt.Errorf("give --workspace-limit and/or --instance-limit"), cli.ExitValidation)
		}
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		var out adminArtifactStorage
		if err := putJSON(client, artifactStoragePath, body, &out); err != nil {
			return err
		}
		return printAdminArtifactStorage(cmd, out)
	},
}

var adminInstanceArtifactStorageRecomputeCmd = &cobra.Command{
	Use:   "recompute",
	Short: "Rebuild the usage counters from the stored artifacts",
	Long: `POST /api/v1/admin/instance/artifact-storage/recompute. Recounts every
artifact family from its source tables in one transaction and reports the drift
per workspace (before and after). Safe to repeat: a second run reports no drift.
Only the derived counters are written; no artifact data is deleted and no limit
changes. --dry-run reports without writing.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		dry, _ := cmd.Flags().GetBool("dry-run")
		var out adminArtifactStorageRecompute
		if err := postJSON(client, artifactStoragePath+"/recompute", map[string]any{"dry_run": dry}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			verb := "Recomputed"
			if out.DryRun {
				verb = "Dry run (nothing written)"
			}
			fmt.Fprintf(w, "%s: %d workspace(s) checked, %d drifted.\n", verb, out.WorkspacesChecked, out.WorkspacesDrifted)
			fmt.Fprintf(w, "Instance used:     %d -> %d bytes\n", out.Instance.UsedBefore, out.Instance.UsedAfter)
			fmt.Fprintf(w, "Instance reserved: %d -> %d bytes\n", out.Instance.ReservedBefore, out.Instance.ReservedAfter)
			for _, ws := range out.Workspaces {
				fmt.Fprintf(w, "  %s: used %d -> %d, reserved %d -> %d\n", ws.WorkspaceID, ws.UsedBefore, ws.UsedAfter, ws.ReservedBefore, ws.ReservedAfter)
			}
			if !out.Changed {
				fmt.Fprintln(w, "Counters already match the stored artifacts.")
			}
			fmt.Fprintln(w, "No artifact data was deleted.")
		})
	},
}

func init() {
	adminInstanceArtifactStorageSetCmd.Flags().String("workspace-limit", "", "Per-workspace limit (bytes or 16GiB)")
	adminInstanceArtifactStorageSetCmd.Flags().String("instance-limit", "", "Whole-instance limit (bytes or 48GiB)")
	adminInstanceArtifactStorageRecomputeCmd.Flags().Bool("dry-run", false, "Report the drift without writing")
	adminInstanceArtifactStorageCmd.AddCommand(adminInstanceArtifactStorageGetCmd, adminInstanceArtifactStorageSetCmd, adminInstanceArtifactStorageRecomputeCmd)
	adminInstanceCmd.AddCommand(adminInstanceArtifactStorageCmd)
}
