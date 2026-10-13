package main

// POST /api/v1/pages/{slug}/artifact/copies page artifact copy

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newPageArtifactCopyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "copy <source-slug> <new-slug>", Short: "Copy a saved artifact revision into a new private artifact", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		revision, _ := cmd.Flags().GetInt64("revision")
		name, _ := cmd.Flags().GetString("name")
		if revision < 1 || strings.TrimSpace(name) == "" {
			return fmt.Errorf("--revision must be positive and --name is required")
		}
		mutation, _ := cmd.Flags().GetString("mutation-id")
		if cmd.Flags().Changed("mutation-id") && strings.TrimSpace(mutation) == "" {
			return fmt.Errorf("--mutation-id must not be empty")
		}
		if mutation == "" {
			mutation = uuid.NewString()
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/copies", map[string]any{"source_revision": revision, "slug": args[1], "name": strings.TrimSpace(name), "mutation_id": mutation})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().Int64("revision", 0, "Required immutable source revision; the current head is not inferred")
	cmd.Flags().String("name", "", "Display name of the new private artifact")
	cmd.Flags().String("mutation-id", "", "Stable identity for retrying this exact copy; generated once when omitted")
	return cmd
}
