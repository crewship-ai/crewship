package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/documents"
)

// runDocumentTarget reads a single explicit selection. The CLI never creates
// submission IDs, guesses a target, or refreshes a baseline on a retry.
func runDocumentTarget(cmd *cobra.Command) (map[string]any, error) {
	if !cmd.Flags().Changed("document-target") {
		return nil, nil
	}
	chat, _ := cmd.Flags().GetString("chat")
	interactive, _ := cmd.Flags().GetBool("interactive")
	versions, _ := cmd.Flags().GetStringArray("project-file-version")
	if !cmd.Flags().Changed("chat") || strings.TrimSpace(chat) == "" {
		return nil, fmt.Errorf("--document-target requires an explicit existing --chat")
	}
	if interactive || len(versions) != 0 {
		return nil, fmt.Errorf("--document-target does not support interactive or project-file-version runs")
	}
	file, _ := cmd.Flags().GetString("document-target")
	if strings.TrimSpace(file) == "" || file == "-" {
		return nil, fmt.Errorf("--document-target requires an intent JSON file")
	}
	// Refuse directories, symlinks and pipes before opening; this flag must
	// never turn into another stdin reader or an unbounded file read.
	stat, err := os.Lstat(file)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 1024 {
		return nil, fmt.Errorf("--document-target requires a regular intent JSON file of at most 1024 bytes")
	}
	handle, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("could not read --document-target file")
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(stat, opened) {
		return nil, fmt.Errorf("--document-target file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(handle, 1025))
	if err != nil {
		return nil, fmt.Errorf("could not read --document-target file")
	}
	intent, err := documents.DecodeIterationIntent(raw)
	if err != nil {
		return nil, fmt.Errorf("--document-target requires one closed Document intent with submission_id, page_id, schema, rev and generation")
	}
	return map[string]any{documents.IterationMetadataKey: &intent}, nil
}

func firstRunMetadata(metadata []map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	return metadata[0]
}
