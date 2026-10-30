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
		use, count := "checkpoints <page-slug>", 1
		short, example := "List retained document checkpoints without changing the document", "  crewship doc checkpoints @<artifact-id>"
		if selected {
			use, count = "checkpoint <page-slug> <checkpoint-id>", 2
			short, example = "Read the confirmed body of one retained checkpoint", "  crewship doc checkpoint @<artifact-id> <checkpoint-id>"
		}
		commands = append(commands, &cobra.Command{
			Use: use, Short: short, Example: example, Args: cobra.ExactArgs(count),
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
	return append(commands, newDocRestoreCommand())
}
