package main

// GET /api/v1/pages/{slug}/document/checkpoints doc checkpoints
// GET /api/v1/pages/{slug}/document/checkpoints/{checkpoint} doc checkpoint

import (
	"fmt"
	"net/url"
	"regexp"

	"github.com/spf13/cobra"
)

func newDocHistoryCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, selected := range []bool{false, true} {
		name, use, count := "checkpoints", "checkpoints <page-slug>", 1
		if selected {
			name, use, count = "checkpoint", "checkpoint <page-slug> <checkpoint-id>", 2
		}
		commands = append(commands, &cobra.Command{
			Use: use, Short: "Read confirmed Document " + name + " without changing the current draft", Args: cobra.ExactArgs(count),
			RunE: func(cmd *cobra.Command, args []string) error {
				path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/document/checkpoints"
				if selected {
					if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(args[1]) {
						return fmt.Errorf("bounded checkpoint identity required")
					}
					path += "/" + url.PathEscape(args[1])
				}
				client, err := pageClient()
				if err != nil {
					return err
				}
				response, err := client.Get(path)
				if err != nil {
					return err
				}
				return printPageArtifactResponse(cmd, response)
			},
		})
	}
	return commands
}
