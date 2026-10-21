package main

// POST /api/v1/pages/{id}/duplicate page duplicate

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newPageDuplicateCommand() *cobra.Command {
	command := &cobra.Command{Use: "duplicate <immutable-page-id>", Short: "Duplicate your personal Document's latest confirmed content into a new private Document", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		key, _ := cmd.Flags().GetString("mutation-id")
		if err := validateEntityCLIIdentifier(args[0]); err != nil {
			return err
		}
		if err := validateEntityCLIIdentifier(key); err != nil || len(key) > 128 {
			return fmt.Errorf("bounded --mutation-id is required; reuse the same value for retries")
		}
		var version *int64
		if cmd.Flags().Changed("pages-version") {
			v, _ := cmd.Flags().GetInt64("pages-version")
			if v < 0 || v >= 9007199254740991 {
				return fmt.Errorf("--pages-version must be a nonnegative safe Page membership version")
			}
			version = &v
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.WithHeader("Idempotency-Key", key).Post("/api/v1/pages/"+url.PathEscape(args[0])+"/duplicate", map[string]any{"mutation_id": key, "expected_source": map[string]any{"id": args[0], "pages_version": version}})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	command.Flags().String("mutation-id", "", "Stable identity for one copy action; reuse unchanged after an unknown result")
	command.Flags().Int64("pages-version", 0, "Expected Page membership version, not Document revision; omitted means confirmed server latest")
	_ = command.MarkFlagRequired("mutation-id")
	return command
}

func init() { pageCmd.AddCommand(newPageDuplicateCommand()) }
