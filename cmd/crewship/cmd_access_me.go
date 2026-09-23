package main

import (
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type actionAccess struct {
	State  string `json:"state" yaml:"state"`
	Reason string `json:"reason" yaml:"reason"`
}

type accessMeResult struct {
	Routine      string                  `json:"routine,omitempty" yaml:"routine,omitempty"`
	CredentialID string                  `json:"credential_id,omitempty" yaml:"credential_id,omitempty"`
	Actions      map[string]actionAccess `json:"actions" yaml:"actions"`
}

func accessMeCommand(kind string) *cobra.Command {
	return &cobra.Command{
		Use:   "access <" + map[string]string{"routine": "slug", "credential": "id"}[kind] + ">",
		Short: "Explain your effective access to this " + kind,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireAuth(); err != nil {
				return err
			}
			if err := requireWorkspace(); err != nil {
				return err
			}
			client := newAPIClient()
			ws := url.PathEscape(client.GetWorkspaceID())
			var endpoint string
			if kind == "routine" {
				endpoint = fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/access/me", ws, url.PathEscape(args[0]))
			} else {
				endpoint = fmt.Sprintf("/api/v1/credentials/%s/access/me?workspace_id=%s", url.PathEscape(args[0]), url.QueryEscape(client.GetWorkspaceID()))
			}
			response, err := client.Get(endpoint)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err := cli.CheckError(response); err != nil {
				return err
			}
			var result accessMeResult
			if err := cli.ReadJSON(response, &result); err != nil {
				return fmt.Errorf("decode access response: %w", err)
			}
			return resolvedFormatter(cmd).AutoHuman(result, func() {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "ACTION\tSTATE\tREASON")
				for _, action := range []string{"read", "run", "edit", "approve", "disable", "replay", "manage_schedules", "rotate", "manage_bindings", "reveal", "delete", "lower_sensitivity"} {
					if decision, ok := result.Actions[action]; ok {
						fmt.Fprintf(w, "%s\t%s\t%s\n", action, decision.State, decision.Reason)
					}
				}
				_ = w.Flush()
			})
		},
	}
}

func init() {
	pipelineCmd.AddCommand(accessMeCommand("routine"))
	credentialCmd.AddCommand(accessMeCommand("credential"))
}
