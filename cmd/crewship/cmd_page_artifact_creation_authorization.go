package main

// POST   /api/v1/artifact-creation-authorizations/{runId}  page artifact authorize-creation
// DELETE /api/v1/artifact-creation-authorizations/{runId}  page artifact revoke-creation
// GET /api/v1/chats/{chatId}/artifact-creation-authorization page artifact creation-status

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPageArtifactCreationAuthorizationCommand(revoke bool) *cobra.Command {
	verb, short := "authorize-creation", "Allow your verified live chat run to create one private artifact in your name"
	if revoke {
		verb, short = "revoke-creation", "Revoke your chat run's permission to create an artifact"
	}
	return &cobra.Command{Use: verb + " <run-id>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		if revoke {
			response, err := client.Delete("/api/v1/artifact-creation-authorizations/" + pagePathEscape(args[0]))
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err := pageCheckError(response); err != nil {
				return err
			}
			return resolvedFormatter(cmd).AutoHuman(map[string]any{"run_id": args[0], "revoked": true}, func() { fmt.Fprintln(cmd.OutOrStdout(), "Artifact creation permission revoked") })
		}
		response, err := client.Post("/api/v1/artifact-creation-authorizations/"+pagePathEscape(args[0]), map[string]any{})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
}

func newPageArtifactCreationStatusCommand() *cobra.Command {
	return &cobra.Command{Use: "creation-status <chat-id>", Short: "Resolve your chat's live run and artifact creation permission on the server", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/chats/" + pagePathEscape(args[0]) + "/artifact-creation-authorization")
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
}
