package main

// POST /api/v1/pages/{slug}/slides/initialize slide initialize

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/slides"
)

func newSlideInitializeCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "initialize <slug|@ID>", Short: "Initialize native Slides on an existing Page", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		filename, _ := cmd.Flags().GetString("file")
		if filename == "" {
			return fmt.Errorf("--file is required; use - for stdin")
		}
		var reader io.Reader = cmd.InOrStdin()
		if filename != "-" {
			file, err := os.Open(filename)
			if err != nil {
				return err
			}
			defer file.Close()
			reader = file
		}
		raw, err := io.ReadAll(io.LimitReader(reader, slides.MaxDeckBytes+1))
		if err != nil {
			return err
		}
		// Validate the exact request without fetching/rebasing identity or retry IDs.
		if _, err = slides.DecodeInitialize(raw); err != nil {
			return fmt.Errorf("invalid Slides initialize request")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/slides/initialize", json.RawMessage(raw))
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().String("file", "", "Exact JSON request; - reads stdin. Preserve page_id, mutation_id and preconditions on retry")
	return cmd
}
