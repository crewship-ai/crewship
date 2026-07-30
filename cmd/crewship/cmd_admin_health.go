//go:build !clionly

package main

// `crewship admin health` — the instance's live vitals: uptime, database
// liveness, disk headroom on the data volume, the current log level, and
// where the encryption master key came from.
//
// GET /api/v1/admin/health has returned all of this since it was written; the
// admin overview rendered two of the five fields and nothing else read it at
// all. An operator triaging "the box feels wedged" had no way to ask from a
// terminal (CLAUDE.md rule 3: an API without a CLI command is an API no agent
// can drive).

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type adminHealthDB struct {
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

type adminHealthDisk struct {
	Path       string  `json:"path,omitempty"`
	Error      string  `json:"error,omitempty"`
	FreeBytes  int64   `json:"free_bytes,omitempty"`
	TotalBytes int64   `json:"total_bytes,omitempty"`
	UsedPct    float64 `json:"used_pct,omitempty"`
}

// adminHealthLogLevel is the live level, the configured baseline, and the
// expiry of a temporary override — a level that reverts in ten minutes is a
// different fact from one someone set for good.
type adminHealthLogLevel struct {
	Level     string  `json:"level"`
	Baseline  string  `json:"baseline"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// adminHealthRow mirrors the map written by api.AdminObservabilityHandler.Health.
type adminHealthRow struct {
	UptimeSeconds       int                  `json:"uptime_seconds"`
	LogLevel            *adminHealthLogLevel `json:"log_level,omitempty"`
	EncryptionKeySource string               `json:"encryption_key_source,omitempty"`
	DB                  *adminHealthDB       `json:"db,omitempty"`
	Disk                *adminHealthDisk     `json:"disk,omitempty"`
}

// humanUptime renders seconds the way an operator reads them.
func humanUptime(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	d, h, m := sec/86400, (sec%86400)/3600, (sec%3600)/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func humanBytes(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "kMGTPE"[exp])
}

// keySourceMeaning turns the enum into the consequence it carries. "generated"
// is not a warning until you know it means the key sits beside the database,
// so a copied disk carries both the ciphertext and what opens it.
func keySourceMeaning(src string) string {
	switch src {
	case "generated":
		return "generated — the key file sits beside the database, so a disk copy carries both"
	case "external":
		return "external — injected by the operator's environment"
	case "":
		return "unknown"
	default:
		return src
	}
}

var adminHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show instance vitals: uptime, database, disk, log level, key source (admin)",
	Long: `Reports what the instance knows about its own health.

Disk is the data directory's volume — the one that fills in practice, since the
database, agent outputs and logs all live under it.

Examples:
  crewship admin health
  crewship admin health --format json | jq '.disk.used_pct'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/health")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var row adminHealthRow
		if err := json.NewDecoder(resp.Body).Decode(&row); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return resolvedFormatter(cmd).AutoHuman(row, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "CHECK\tSTATE")
			fmt.Fprintf(w, "Uptime\t%s\n", humanUptime(row.UptimeSeconds))

			db := "unknown"
			if row.DB != nil {
				if row.DB.Connected {
					db = "connected"
				} else {
					db = "UNREACHABLE"
					if row.DB.Error != "" {
						db += " — " + row.DB.Error
					}
				}
			}
			fmt.Fprintf(w, "Database\t%s\n", db)

			// Absent disk info stays absent: printing "0 B free" where statfs
			// failed would invent an emergency out of a missing measurement.
			switch {
			case row.Disk == nil:
				fmt.Fprintf(w, "Disk\tnot reported\n")
			case row.Disk.Error != "":
				fmt.Fprintf(w, "Disk\tunavailable — %s\n", row.Disk.Error)
			default:
				fmt.Fprintf(w, "Disk\t%.0f%% used · %s free of %s (%s)\n",
					row.Disk.UsedPct, humanBytes(row.Disk.FreeBytes),
					humanBytes(row.Disk.TotalBytes), row.Disk.Path)
			}

			if row.LogLevel != nil && row.LogLevel.Level != "" {
				lvl := row.LogLevel.Level
				// An override that expires is temporary state, and an operator
				// reading "debug" needs to know whether it reverts on its own.
				if row.LogLevel.ExpiresAt != nil {
					lvl = fmt.Sprintf("%s (temporary — reverts to %s at %s)",
						lvl, row.LogLevel.Baseline, *row.LogLevel.ExpiresAt)
				} else if row.LogLevel.Baseline != "" && row.LogLevel.Baseline != lvl {
					lvl = fmt.Sprintf("%s (baseline %s)", lvl, row.LogLevel.Baseline)
				}
				fmt.Fprintf(w, "Log level\t%s\n", lvl)
			}
			fmt.Fprintf(w, "Encryption key\t%s\n", keySourceMeaning(row.EncryptionKeySource))
			_ = w.Flush()
		})
	},
}

func init() {
	adminCmd.AddCommand(adminHealthCmd)
}
