package main

// GET /api/v1/pages/{slug}/slides slide read

import "github.com/spf13/cobra"

func newSlideReadCommand() *cobra.Command {
	group := &cobra.Command{Use: "slide", Short: "Read or initialize saved native Slides"}
	group.AddCommand(&cobra.Command{Use: "read <slug|@ID>", Short: "Read the current saved native deck", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/slides")
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}})
	group.AddCommand(newSlideInitializeCommand())
	return group
}
func init() { rootCmd.AddCommand(newSlideReadCommand()) }
