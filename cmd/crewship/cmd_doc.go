package main

// GET /api/v1/documents/status doc status
// POST /api/v1/pages/{slug}/document/initialize doc initialize
// GET /api/v1/pages/{slug}/document doc read, doc outline
// POST /api/v1/pages/{slug}/document/patch doc patch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func newDocCommand() *cobra.Command {
	root := &cobra.Command{Use: "doc", Short: "Read and edit saved Page documents with semantic preconditions"}
	for _, operation := range []string{"read", "outline", "patch", "status", "initialize"} {
		operation := operation
		cmd := &cobra.Command{Use: operation + " <page-slug>", Short: "Document " + operation, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			client, err := pageClient()
			if err != nil {
				return err
			}
			var response *http.Response
			switch operation {
			case "read", "outline":
				path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/document"
				response, err = client.Get(path)
			case "status":
				response, err = client.Get("/api/v1/documents/status")
			default:
				filename, _ := cmd.Flags().GetString("file")
				if filename == "" {
					return fmt.Errorf("--file is required; use - for stdin")
				}
				var reader io.Reader = cmd.InOrStdin()
				if filename != "-" {
					file, openErr := os.Open(filename)
					if openErr != nil {
						return openErr
					}
					defer file.Close()
					reader = file
				}
				data, readErr := io.ReadAll(io.LimitReader(reader, (2<<20)+1))
				if readErr != nil {
					return readErr
				}
				if len(data) > 2<<20 {
					return fmt.Errorf("document request exceeds 2 MiB")
				}
				var body map[string]json.RawMessage
				decoder := json.NewDecoder(bytes.NewReader(data))
				if err := decoder.Decode(&body); err != nil || body == nil {
					return fmt.Errorf("--file must contain one JSON object")
				}
				if decoder.Decode(new(any)) != io.EOF {
					return fmt.Errorf("--file must contain one JSON object")
				}
				switch operation {
				case "patch":
					response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/patch", body)
				case "initialize":
					response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/initialize", body)
				default:
					return fmt.Errorf("unknown document operation %q", operation)
				}
			}
			if err != nil {
				return err
			}
			if operation != "outline" {
				return printPageArtifactResponse(cmd, response)
			}
			defer response.Body.Close()
			if err := pageCheckError(response); err != nil {
				return err
			}
			var state struct {
				Outline json.RawMessage `json:"outline" yaml:"outline"`
			}
			if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&state); err != nil {
				return err
			}
			if len(state.Outline) == 0 {
				return fmt.Errorf("server response has no document outline")
			}
			var outline any
			if err := json.Unmarshal(state.Outline, &outline); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(outline)
		}}
		if operation == "status" {
			cmd.Use = "status"
			cmd.Args = cobra.NoArgs
		}
		if operation == "patch" || operation == "initialize" {
			cmd.Flags().String("file", "", "JSON request file; - reads stdin. Preserve operation IDs when retrying an exact request")
		}
		root.AddCommand(cmd)
	}
	root.AddCommand(newDocCreateCommand())
	return root
}

func init() { rootCmd.AddCommand(newDocCommand()) }
