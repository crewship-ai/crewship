package main

// GET   /api/v1/pages/{slug}/artifact/threads                    page artifact threads list
// POST  /api/v1/pages/{slug}/artifact/threads                    page artifact threads create
// POST  /api/v1/pages/{slug}/artifact/threads/{threadId}/replies   page artifact threads reply
// PATCH /api/v1/pages/{slug}/artifact/threads/{threadId}           page artifact threads resolve
// PATCH /api/v1/pages/{slug}/artifact/threads/{threadId}           page artifact threads reopen

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newPageArtifactThreadsCommand() *cobra.Command {
	group := &cobra.Command{Use: "threads", Short: "Discuss a specific artifact revision without changing its source"}
	for _, verb := range []string{"list", "create", "reply", "resolve", "reopen"} {
		verb := verb
		use, nargs := verb+" <slug>", 1
		if verb == "reply" || verb == "resolve" || verb == "reopen" {
			use, nargs = verb+" <slug> <thread-id>", 2
		}
		cmd := &cobra.Command{Use: use, Short: map[string]string{
			"list":    "Read artifact threads, optionally at a historical revision",
			"create":  "Comment on a complete immutable artifact revision",
			"reply":   "Reply using the expected thread version",
			"resolve": "Resolve a thread using its expected version",
			"reopen":  "Reopen a thread using its expected version",
		}[verb], Args: cobra.ExactArgs(nargs), RunE: func(cmd *cobra.Command, args []string) error {
			revision, _ := cmd.Flags().GetInt64("revision")
			if (verb == "create" || cmd.Flags().Changed("revision")) && revision < 1 {
				return fmt.Errorf("--revision must be specified and positive")
			}
			payload := map[string]any{}
			if verb == "create" {
				payload["revision"] = revision
				payload["anchor"] = map[string]any{"schema_version": 1, "type": "document"}
			}
			if nargs == 2 {
				base, _ := cmd.Flags().GetInt64("base-version")
				if !cmd.Flags().Changed("base-version") || base < 1 {
					return fmt.Errorf("--base-version must be specified and positive")
				}
				payload["base_version"] = base
			}
			if verb == "create" || verb == "reply" {
				body, _ := cmd.Flags().GetString("body")
				if strings.TrimSpace(body) == "" || !utf8.ValidString(body) {
					return fmt.Errorf("--body must contain nonempty UTF-8 text")
				}
				if len(body) > 4096 {
					return fmt.Errorf("--body exceeds 4 KiB")
				}
				payload["body"] = body
			}
			if verb != "list" {
				mutation, _ := cmd.Flags().GetString("mutation-id")
				if cmd.Flags().Changed("mutation-id") && strings.TrimSpace(mutation) == "" {
					return fmt.Errorf("--mutation-id must not be empty")
				}
				if mutation == "" {
					mutation = uuid.NewString()
				}
				payload["mutation_id"] = mutation
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			var response *http.Response
			switch verb {
			case "list":
				endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact/threads"
				if revision > 0 {
					endpoint += fmt.Sprintf("?revision=%d", revision)
				}
				response, err = client.Get(endpoint)
			case "create":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/threads", payload)
			case "reply":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/threads/"+pagePathEscape(args[1])+"/replies", payload)
			default:
				payload["resolved"] = verb == "resolve"
				response, err = client.Patch("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/threads/"+pagePathEscape(args[1]), payload)
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if verb == "list" || verb == "create" {
			cmd.Flags().Int64("revision", 0, "Immutable artifact revision to discuss or filter")
		}
		if nargs == 2 {
			cmd.Flags().Int64("base-version", 0, "Required current thread version; stale writes are rejected")
		}
		if verb == "create" || verb == "reply" {
			cmd.Flags().String("body", "", "Comment text (does not dispatch or authorize an agent)")
		}
		if verb != "list" {
			cmd.Flags().String("mutation-id", "", "Stable identity for retrying this exact operation; generated when omitted")
		}
		group.AddCommand(cmd)
	}
	return group
}
