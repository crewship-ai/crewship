//go:build !clionly

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/spf13/cobra"
)

func newDoctorExecutionOriginsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "execution-origins", Short: "Read-only local inventory of potentially fenced execution authority", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		if limit < 1 || limit > 1000 {
			return fmt.Errorf("--limit must be between 1 and 1000")
		}
		jsonFlag, _ := cmd.Flags().GetBool("json")
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		db, err := openLocalDBReadOnly(ctx)
		if err != nil {
			return fmt.Errorf("execution_origins_database_unavailable: %w", err)
		}
		defer db.Close()
		report, err := runauthority.AuditExecutionOrigins(ctx, db, limit)
		if err != nil {
			return fmt.Errorf("execution_origins_audit_unavailable: %w", err)
		}
		if jsonFlag {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		return resolvedFormatter(cmd).Machine(report)
	}}
	cmd.Flags().Int("limit", 200, "Maximum record IDs to inspect and display (1–1000; totals cover all candidates)")
	cmd.Flags().Bool("json", false, "Emit the read-only audit as JSON")
	return cmd
}
func init() { doctorCmd.AddCommand(newDoctorExecutionOriginsCmd()) }
