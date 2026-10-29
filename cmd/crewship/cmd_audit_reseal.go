package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func newAuditResealCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "reseal", Short: "Explicitly attest a new audit segment without rewriting history", Long: `Create an explicit OWNER checkpoint naming the old tail and a reason.
Old rows and their MACs remain unchanged. A missing historical key remains
historical_unverified: a new segment is not proof of the old history.
Requires the current instance OWNER or trusted workspace OWNER.`, Args: cobra.NoArgs}
	cmd.Flags().String("scope", "workspace", "workspace or instance")
	cmd.Flags().Int64("expected-tail-seq", -1, "Exact reviewed current tail sequence")
	cmd.Flags().String("expected-tail-hash", "", "Exact reviewed current tail hash")
	cmd.Flags().String("reason", "", "Explicit reason (8–512 characters)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		scope, _ := cmd.Flags().GetString("scope")
		seq, _ := cmd.Flags().GetInt64("expected-tail-seq")
		hash, _ := cmd.Flags().GetString("expected-tail-hash")
		reason, _ := cmd.Flags().GetString("reason")
		reason = strings.TrimSpace(reason)
		if (scope != "workspace" && scope != "instance") || seq < 0 || (seq > 0 && hash == "") || len(hash) > 128 || len(reason) < 8 || len(reason) > 512 {
			return fmt.Errorf("scope, exact tail sequence/hash and a reason of 8–512 characters are required")
		}
		client := newAPIClient()
		workspace := ""
		if scope == "workspace" {
			if err := requireWorkspace(); err != nil {
				return err
			}
			var err error
			workspace, err = client.ResolveWorkspaceIDStrict(cmd.Context())
			if err != nil {
				return err
			}
		} else {
			client.WorkspaceID = ""
		}
		body := map[string]any{"scope": scope, "expected_tail_seq": seq, "expected_tail_hash": hash, "reason": reason}
		if workspace != "" {
			body["workspace_id"] = workspace
		}
		response, err := client.Post("/api/v1/admin/audit/reseal", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(response); err != nil {
			return err
		}
		var result struct {
			Scope                string `json:"scope" yaml:"scope"`
			WorkspaceID          string `json:"workspace_id" yaml:"workspace_id"`
			CheckpointID         string `json:"checkpoint_id" yaml:"checkpoint_id"`
			TailSeq              int64  `json:"tail_seq" yaml:"tail_seq"`
			TailHash             string `json:"tail_hash" yaml:"tail_hash"`
			ContinuationVerified bool   `json:"continuation_verified" yaml:"continuation_verified"`
			HistoricalStatus     string `json:"historical_status" yaml:"historical_status"`
		}
		if err := cli.ReadJSON(response, &result); err != nil {
			return err
		}
		if !result.ContinuationVerified || result.CheckpointID == "" {
			return fmt.Errorf("server did not acknowledge the explicit checkpoint")
		}
		return newFormatter().AutoHuman(result, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "New audit segment checkpoint %s committed. Historical status: %s. Old MACs were not rewritten.\n", result.CheckpointID, result.HistoricalStatus)
		})
	}
	return cmd
}

func init() { auditCmd.AddCommand(newAuditResealCommand()) }
