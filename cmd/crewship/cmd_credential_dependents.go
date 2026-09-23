package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var credDependentsCmd = &cobra.Command{
	Use:   "dependents <name-or-id>",
	Short: "Show routines that declare this credential type",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		id, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/credentials/" + id + "/dependents")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Routines []struct {
				Slug       string `json:"slug" yaml:"slug"`
				Name       string `json:"name" yaml:"name"`
				Resolution string `json:"resolution" yaml:"resolution"`
			} `json:"routines" yaml:"routines"`
			VisibilityLimited bool `json:"visibility_limited" yaml:"visibility_limited"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		rows := make([][]string, 0, len(body.Routines))
		for _, r := range body.Routines {
			rows = append(rows, []string{r.Slug, r.Name, r.Resolution})
		}
		if err := newFormatter().Auto(body, []string{"SLUG", "NAME", "CURRENT SELECTION"}, rows); err != nil {
			return err
		}
		if body.VisibilityLimited {
			fmt.Fprintln(cmd.ErrOrStderr(), "Hidden routines are excluded from your view.")
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "References are configured intent, not recorded run use; dynamic agent/script lookups are not included.")
		return nil
	},
}

func init() { credentialCmd.AddCommand(credDependentsCmd) }
