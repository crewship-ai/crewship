package main

// POST /api/v1/pages/{slug}/document/presence doc presence

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/spf13/cobra"
)

var docPresenceClient = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func newDocPresenceCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "presence <page-slug>", Short: "Show who else has a document open (one presence heartbeat)", Long: `Send one presence heartbeat for this tab id and print the other people on the
document: their user id, the name the server resolved for them, their colour
index and their caret as encoded Yjs relative positions. Presence is ephemeral:
the server keeps it in memory for a few seconds and never stores it. Every
heartbeat is authorized like a document read, so a revoked reader is refused
(404) and removed. --leave removes this tab at once.`, Example: `  crewship doc presence @<artifact-id>
  crewship doc presence @<artifact-id> --client my-script-tab --leave`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetString("client")
		if id == "" {
			raw := make([]byte, 8)
			if _, err := rand.Read(raw); err != nil {
				return err
			}
			id = "cli-" + hex.EncodeToString(raw)
		}
		if !docPresenceClient.MatchString(id) {
			return fmt.Errorf("--client must be 8 to 64 letters, digits, - or _")
		}
		leave, _ := cmd.Flags().GetBool("leave")
		body := map[string]any{"client": id}
		if leave {
			body["leave"] = true
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/presence", body)
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().String("client", "", "Tab id to heartbeat as (8-64 of A-Z a-z 0-9 - _); default: a new random id")
	cmd.Flags().Bool("leave", false, "Remove this tab from the document's presence")
	return cmd
}
