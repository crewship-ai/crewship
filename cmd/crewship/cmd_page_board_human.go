package main

// GET /api/v1/pages/{slug}/board page board show
// POST /api/v1/pages/{slug}/board page board init

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// This read/create adapter keeps the established Board command names. A
// stable mutation ID belongs to the caller and is never replaced on retries.
func newPageBoardHumanCommand() *cobra.Command {
	root := &cobra.Command{Use: "board", Short: "Initialize and read private empty Boards", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	for _, op := range []string{"show", "init"} {
		operation := op
		cmd := &cobra.Command{Use: op + " <page-slug-or-@id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			var body any
			method := http.MethodGet
			if operation == "init" {
				mutation, _ := cmd.Flags().GetString("mutation-id")
				if mutation == "" || strings.TrimSpace(mutation) != mutation || len(mutation) > 128 {
					return fmt.Errorf("--mutation-id must be a nonempty stable ID of at most 128 bytes; reuse it for identical retries")
				}
				body = map[string]string{"mutation_id": mutation}
				method = http.MethodPost
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			response, err := client.Do(method, "/api/v1/pages/"+pagePathEscape(args[0])+"/board", body)
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if operation == "init" {
			cmd.Flags().String("mutation-id", "", "Required stable identity for initialization and identical retries")
		}
		root.AddCommand(cmd)
	}
	return root
}

func init() { pageCmd.AddCommand(newPageBoardHumanCommand()) }
