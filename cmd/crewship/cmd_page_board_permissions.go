package main

// GET /api/v1/pages/{slug}/board/element-permissions page board permissions list
// POST /api/v1/pages/{slug}/board/element-permissions page board permissions grant
// DELETE /api/v1/pages/{slug}/board/element-permissions/{permissionId} page board permissions revoke
import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func newPageBoardPermissionsCommand() *cobra.Command {
	root := &cobra.Command{Use: "permissions", Short: "Inspect, grant or revoke exact one-batch human-element permissions"}
	for _, verb := range []string{"list", "grant", "revoke"} {
		verb := verb
		argc := 1
		use := verb + " <slug>"
		if verb == "revoke" {
			argc = 2
			use += " <permission-id>"
		}
		cmd := &cobra.Command{Use: use, Args: cobra.ExactArgs(argc), RunE: func(cmd *cobra.Command, args []string) error {
			var body any
			if verb == "grant" {
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
				key := "mutation_id"
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
				response, err = client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/board/element-permissions")
			case "grant":
				response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/board/element-permissions", body)
			case "revoke":
				response, err = client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/board/element-permissions/" + pagePathEscape(args[1]))
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if verb == "grant" {
			cmd.Flags().String("file", "", "Exact JSON request file, or - for stdin; retain IDs for identical retries")
		}
		root.AddCommand(cmd)
	}
	return root
}
