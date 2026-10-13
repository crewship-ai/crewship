package main

// GET /api/v1/pages/{slug}/board/requests page board requests list
// POST /api/v1/pages/{slug}/board/requests page board requests send
// GET /api/v1/pages/{slug}/board/requests/{requestId} page board requests show
// POST /api/v1/pages/{slug}/board/requests/{requestId}/cancel page board requests cancel
// POST /api/v1/pages/{slug}/board/requests/{requestId}/insert page board requests insert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func newPageBoardRequestsCommand() *cobra.Command {
	root := &cobra.Command{Use: "requests", Short: "Explicit Board selection jobs and confirmed diagram insertion"}
	for _, verb := range []string{"list", "show", "send", "cancel", "insert"} {
		verb := verb
		argc := 1
		use := verb + " <slug>"
		if verb == "show" || verb == "cancel" || verb == "insert" {
			argc = 2
			use += " <request-id>"
		}
		cmd := &cobra.Command{Use: use, Args: cobra.ExactArgs(argc), RunE: func(cmd *cobra.Command, args []string) error {
			var body any
			if verb == "send" || verb == "insert" {
				file, _ := cmd.Flags().GetString("file")
				if file == "" {
					return fmt.Errorf("--file is required; use - for stdin")
				}
				var reader io.Reader = cmd.InOrStdin()
				if file != "-" {
					input, err := os.Open(file)
					if err != nil {
						return err
					}
					defer input.Close()
					reader = input
				}
				limit := int64(64 << 10)
				if verb == "insert" {
					limit = 256 << 10
				}
				raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
				if err != nil {
					return err
				}
				if int64(len(raw)) > limit {
					return fmt.Errorf("request JSON exceeds %d bytes", limit)
				}
				var document map[string]json.RawMessage
				if json.Unmarshal(raw, &document) != nil || document == nil {
					return fmt.Errorf("one JSON object required")
				}
				key := "submission_id"
				if verb == "insert" {
					key = "mutation_id"
				}
				var identity string
				if json.Unmarshal(document[key], &identity) != nil || identity == "" {
					return fmt.Errorf("%s required; retain it for identical retries", key)
				}
				body = bytes.NewReader(raw)
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			var response *http.Response
			switch verb {
			case "list":
				response, err = client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/board/requests")
			case "show":
				response, err = client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/board/requests/" + pagePathEscape(args[1]))
			case "send":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/board/requests", body)
			case "cancel":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/board/requests/"+pagePathEscape(args[1])+"/cancel", map[string]any{})
			case "insert":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/board/requests/"+pagePathEscape(args[1])+"/insert", body)
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if verb == "send" || verb == "insert" {
			cmd.Flags().String("file", "", "Exact JSON request file, or - for stdin; retain IDs for identical retries")
		}
		root.AddCommand(cmd)
	}
	return root
}
