package main

// GET /api/v1/pages/{slug}/slides/requests slide request list
// GET /api/v1/pages/{slug}/slides/requests/{requestId} slide request get
// POST /api/v1/pages/{slug}/slides/requests slide request create
// POST /api/v1/pages/{slug}/slides/requests/{requestId}/cancel slide request cancel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/slides"
)

func newSlideRequestCommand() *cobra.Command {
	root := &cobra.Command{Use: "request", Short: "Admit and inspect immutable selected-text agent requests"}
	for _, operation := range []string{"list", "get", "create", "cancel"} {
		command := &cobra.Command{Use: operation + " <page-slug>", Short: "Slides request " + operation, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			client, err := pageClient()
			if err != nil {
				return err
			}
			path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/slides/requests"
			var response *http.Response
			switch operation {
			case "list":
				limit, _ := cmd.Flags().GetInt("limit")
				cursor, _ := cmd.Flags().GetString("cursor")
				if limit < 1 || limit > 100 || len(cursor) > 1024 {
					return errors.New("bounded limit1..100 and cursor required")
				}
				query := url.Values{"limit": []string{strconv.Itoa(limit)}}
				if cursor != "" {
					query.Set("cursor", cursor)
				}
				response, err = client.Get(path + "?" + query.Encode())
			case "get":
				response, err = client.Get(path + "/" + pagePathEscape(args[1]))
			default:
				filename, _ := cmd.Flags().GetString("file")
				if filename == "" {
					return errors.New("--file is required; use - for stdin")
				}
				reader := cmd.InOrStdin()
				if filename != "-" {
					file, err := os.Open(filename)
					if err != nil {
						return err
					}
					defer file.Close()
					reader = file
				}
				raw, err := io.ReadAll(io.LimitReader(reader, slides.MaxSelectedRequestBytes+1))
				if err != nil {
					return err
				}
				switch operation {
				case "create":
					if _, err = slides.DecodeSelectedRequest(raw); err != nil {
						return err
					}
					response, err = client.Post(path, json.RawMessage(raw))
				case "cancel":
					if _, err = slides.DecodeSelectedCancel(raw); err != nil {
						return err
					}
					response, err = client.Post(path+"/"+pagePathEscape(args[1])+"/cancel", json.RawMessage(raw))
				}
				if err != nil {
					return err
				}
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if operation == "get" || operation == "cancel" {
			command.Use = operation + " <page-slug> <request-id>"
			command.Args = cobra.ExactArgs(2)
		}
		if operation == "create" || operation == "cancel" {
			command.Flags().String("file", "", "Exact immutable JSON admission/cancellation; - reads stdin. No refresh on conflict")
		}
		if operation == "list" {
			command.Flags().Int("limit", 25, "Bounded page size, 1..100")
			command.Flags().String("cursor", "", "Opaque continuation cursor from the previous response")
		}
		root.AddCommand(command)
	}
	return root
}
