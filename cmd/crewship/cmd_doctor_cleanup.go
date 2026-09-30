//go:build !clionly

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/spf13/cobra"
)

type localCleanupSnapshot struct {
	InstanceID               string `json:"instance_id" yaml:"instance_id"`
	WorkspaceID              string `json:"workspace_id" yaml:"workspace_id"`
	resourcelifecycle.Status `json:",inline" yaml:",inline"`
	Stale                    bool `json:"stale" yaml:"stale"`
}

// Local file permissions, rather than deleted workspace membership, authorize
// this diagnostic. Existing readonly open resolves DATABASE_URL and never
// creates a database or applies migrations. This command never contacts Docker.
func readLocalCleanup(ctx context.Context) ([]localCleanupSnapshot, error) {
	db, err := openLocalDBReadOnly(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	// Owner rows change only on transitions; the installation scan row carries
	// when they were last confirmed and whether that scan was complete.
	rows, err := db.QueryContext(ctx, `SELECT s.instance_id,s.workspace_id,s.crew_id,s.state,COALESCE(sc.observed_at,''),COALESCE(sc.complete,0),s.remaining,s.unattributed,
 CASE WHEN s.error_code<>'' THEN s.error_code ELSE COALESCE(sc.error_code,'') END
 FROM resource_cleanup_status s LEFT JOIN resource_cleanup_scans sc ON sc.instance_id=s.instance_id ORDER BY s.instance_id,s.crew_id`)
	if err != nil {
		return nil, fmt.Errorf("container cleanup diagnostics unavailable (schema may predate cleanup): %w", err)
	}
	defer rows.Close()
	out := []localCleanupSnapshot{}
	for rows.Next() {
		s := localCleanupSnapshot{Status: resourcelifecycle.Status{Scope: "containers"}}
		if err := rows.Scan(&s.InstanceID, &s.WorkspaceID, &s.CrewID, &s.State, &s.ObservedAt, &s.Complete, &s.Remaining, &s.Unattributed, &s.Error); err != nil {
			return nil, err
		}
		observed, err := time.Parse(time.RFC3339, s.ObservedAt)
		s.Stale = err != nil || !s.Complete || time.Since(observed) > resourcelifecycle.StaleAfter
		// All local results are historical observations, never live host clearance.
		if s.Stale {
			s.State = "unknown"
			s.Complete = false
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func newDoctorCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cleanup", Short: "Read persisted container cleanup diagnostics from the local database", Args: cobra.NoArgs}
	cmd.Flags().Bool("json", false, "Deprecated alias for --format json")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		snapshots, err := readLocalCleanup(cmd.Context())
		if err != nil {
			return err
		}
		format := resolvedFormat(cmd)
		if format == "json" {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"scope": "containers", "observation": "persisted", "items": snapshots})
		}
		if format != "table" {
			return fmt.Errorf("doctor cleanup supports --format table|json (got %q)", format)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Persisted container observations (volumes and host data excluded):")
		for _, s := range snapshots {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s remaining=%d unattributed=%d error=%s observed=%s\n", s.InstanceID, s.CrewID, s.State, s.Remaining, s.Unattributed, s.Error, s.ObservedAt)
		}
		return nil
	}
	return cmd
}
func init() { doctorCmd.AddCommand(newDoctorCleanupCmd()) }
