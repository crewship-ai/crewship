package main

// GET /api/v1/pages/{slug}/slides/comments slide comments list
// GET /api/v1/pages/{slug}/slides/comments/{threadId} slide comments get
// GET /api/v1/pages/{slug}/slides/comments/{threadId}/history slide comments history
// POST /api/v1/pages/{slug}/slides/comments slide comments create
// POST /api/v1/pages/{slug}/slides/comments/{threadId}/replies slide comments reply
// POST /api/v1/pages/{slug}/slides/comments/{threadId}/resolve slide comments resolve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/spf13/cobra"
)

func newSlideCommentsCommand() *cobra.Command {
	root := &cobra.Command{Use: "comments", Short: "Human comments anchored to stable Slides identities"}
	for _, operation := range []string{"list", "get", "history", "create", "reply", "resolve"} {
		read := operation == "list" || operation == "get" || operation == "history"
		thread := operation != "list" && operation != "create"
		cmd := &cobra.Command{Use: operation + " <page-slug>", Short: "Slides comments " + operation, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			client, err := pageClient()
			if err != nil {
				return err
			}
			path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/slides/comments"
			var response *http.Response
			if read {
				query := ""
				if operation != "get" {
					limit, _ := cmd.Flags().GetInt("limit")
					cursor, _ := cmd.Flags().GetString("cursor")
					if limit < 1 || limit > slides.MaxCommentPageSize || len(cursor) > 1024 {
						return fmt.Errorf("limit must be 1..100 and cursor at most1024 bytes")
					}
					values := url.Values{}
					values.Set("limit", strconv.Itoa(limit))
					if cursor != "" {
						values.Set("cursor", cursor)
					}
					query = "?" + values.Encode()
				}
				switch operation {
				case "list":
					response, err = client.Get(path + query)
				case "get":
					response, err = client.Get(path + "/" + pagePathEscape(args[1]))
				case "history":
					response, err = client.Get(path + "/" + pagePathEscape(args[1]) + "/history" + query)
				}
			} else {
				filename, _ := cmd.Flags().GetString("file")
				if filename == "" {
					return fmt.Errorf("--file is required; use - for stdin")
				}
				var reader io.Reader = cmd.InOrStdin()
				if filename != "-" {
					file, e := os.Open(filename)
					if e != nil {
						return e
					}
					defer file.Close()
					reader = file
				}
				raw, e := io.ReadAll(io.LimitReader(reader, slides.MaxCommentRequestBytes+1))
				if e != nil {
					return e
				}
				switch operation {
				case "create":
					_, err = slides.DecodeCreateComment(raw)
				case "reply":
					_, err = slides.DecodeReplyComment(raw)
				case "resolve":
					_, err = slides.DecodeResolveComment(raw)
				}
				if err != nil {
					return err
				}
				// Exact frozen page_id/anchor/version/mutation_id are transported once.
				// Conflicts and revoked authority never trigger a refresh or rebase.
				switch operation {
				case "create":
					response, err = client.Post(path, json.RawMessage(raw))
				case "reply":
					response, err = client.Post(path+"/"+pagePathEscape(args[1])+"/replies", json.RawMessage(raw))
				case "resolve":
					response, err = client.Post(path+"/"+pagePathEscape(args[1])+"/resolve", json.RawMessage(raw))
				}
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if thread {
			cmd.Use = operation + " <page-slug> <thread-id>"
			cmd.Args = cobra.ExactArgs(2)
		}
		if read && operation != "get" {
			cmd.Flags().Int("limit", 25, "Bounded page size, 1..100")
			cmd.Flags().String("cursor", "", "Opaque continuation cursor from previous response")
		}
		if !read {
			cmd.Flags().String("file", "", "Exact immutable JSON request; - reads stdin")
		}
		root.AddCommand(cmd)
	}
	return root
}
