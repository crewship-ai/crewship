package main

// PATCH  /api/v1/pages/{slug}/artifact/threads/{threadId}/comments/{commentId}                   page artifact comments edit
// DELETE /api/v1/pages/{slug}/artifact/threads/{threadId}/comments/{commentId}                   page artifact comments delete
// PUT    /api/v1/pages/{slug}/artifact/threads/{threadId}/comments/{commentId}/reactions/{emoji} page artifact comments react
// GET    /api/v1/pages/{slug}/artifact/threads/{threadId}/comments/{commentId}/history           page artifact comments history

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newPageArtifactCommentsCommand() *cobra.Command {
	group := &cobra.Command{Use: "comments", Short: "Edit your artifact comments, react, or read your comment history"}
	for _, verb := range []string{"edit", "delete", "react", "history"} {
		verb := verb
		cmd := &cobra.Command{Use: verb + " <slug> <thread-id> <comment-id>", Short: map[string]string{"edit": "Edit your human-authored comment with thread CAS", "delete": "Delete your human-authored comment while retaining private history", "react": "Add or remove a reaction with thread CAS", "history": "Read your private versioned comment history"}[verb], Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
			payload := map[string]any{}
			if verb != "history" {
				base, _ := cmd.Flags().GetInt64("base-version")
				if !cmd.Flags().Changed("base-version") || base < 1 {
					return fmt.Errorf("--base-version must be specified and positive (current thread version)")
				}
				payload["base_version"] = base
				mutation, _ := cmd.Flags().GetString("mutation-id")
				if cmd.Flags().Changed("mutation-id") && strings.TrimSpace(mutation) == "" {
					return fmt.Errorf("--mutation-id must not be empty")
				}
				if mutation == "" {
					mutation = uuid.NewString()
				}
				payload["mutation_id"] = mutation
			}
			if verb == "edit" {
				body, _ := cmd.Flags().GetString("body")
				if strings.TrimSpace(body) == "" || !utf8.ValidString(body) {
					return fmt.Errorf("--body must contain nonempty UTF-8 text")
				}
				if len(body) > 4096 {
					return fmt.Errorf("--body exceeds 4 KiB")
				}
				payload["body"] = body
			}
			emoji := ""
			if verb == "react" {
				emoji, _ = cmd.Flags().GetString("emoji")
				switch emoji {
				case "👍", "❤️", "👀", "✅":
				default:
					return fmt.Errorf("--emoji must be one of 👍 ❤️ 👀 ✅")
				}
				if !cmd.Flags().Changed("active") {
					return fmt.Errorf("--active=true or --active=false must be specified")
				}
				active, _ := cmd.Flags().GetBool("active")
				payload["active"] = active
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact/threads/" + pagePathEscape(args[1]) + "/comments/" + pagePathEscape(args[2])
			var response *http.Response
			switch verb {
			case "edit":
				response, err = client.Patch(endpoint, payload)
			case "delete":
				response, err = client.Do(http.MethodDelete, endpoint, payload)
			case "react":
				response, err = client.Put(endpoint+"/reactions/"+pagePathEscape(emoji), payload)
			case "history":
				response, err = client.Get(endpoint + "/history")
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if verb != "history" {
			cmd.Flags().Int64("base-version", 0, "Required current thread version; stale writes are rejected")
			cmd.Flags().String("mutation-id", "", "Stable identity for retrying this exact operation; generated when omitted")
		}
		if verb == "edit" {
			cmd.Flags().String("body", "", "Replacement comment text, up to 4 KiB UTF-8")
		}
		if verb == "react" {
			cmd.Flags().String("emoji", "", "Reaction: 👍 ❤️ 👀 ✅")
			cmd.Flags().Bool("active", false, "Required: true adds the reaction; false removes it")
		}
		group.AddCommand(cmd)
	}
	return group
}
