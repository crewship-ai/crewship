package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	for _, action := range []string{"reissue", "revoke"} {
		command := &cobra.Command{Use: action + " <invitation-id>", Short: map[string]string{"reissue": "Rotate a pending invitation and return a new recipient link", "revoke": "Revoke a pending invitation without changing memberships"}[action], Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireAuth(); err != nil {
				return err
			}
			if err := requireWorkspace(); err != nil {
				return err
			}
			client := newAPIClient()
			path := "/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) + "/invitations/" + url.PathEscape(args[0])
			var result struct {
				ID    string `json:"id,omitempty" yaml:"id,omitempty"`
				Token string `json:"token,omitempty" yaml:"token,omitempty"`
				Email string `json:"email,omitempty" yaml:"email,omitempty"`
				Role  string `json:"role,omitempty" yaml:"role,omitempty"`
				OK    bool   `json:"ok,omitempty" yaml:"ok,omitempty"`
			}
			var response *http.Response
			var err error
			if action == "reissue" {
				response, err = client.Post(path+"/reissue", nil)
			} else {
				response, err = client.Delete(path)
			}
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err := cli.CheckError(response); err != nil {
				return err
			}
			if err := cli.ReadJSON(response, &result); err != nil {
				return err
			}
			return resolvedFormatter(cmd).AutoHuman(result, func() {
				if action == "revoke" {
					cli.PrintSuccess("Pending invitation revoked. Existing memberships are unchanged.")
					return
				}
				cli.PrintWarning("Previous link revoked. No email was sent; share the new link only with its intended recipient.")
				cmd.Println("Invitation link: " + strings.TrimRight(client.BaseURL, "/") + "/join#invitation_token=" + url.QueryEscape(result.Token))
			})
		}}
		if action == "reissue" {
			command.Aliases = []string{"resend"}
		}
		workspaceInviteCmd.AddCommand(command)
	}
}
