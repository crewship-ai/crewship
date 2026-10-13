package main

// GET /api/v1/pages/{slug}/board page board show
// POST /api/v1/pages/{slug}/board page board init
// POST /api/v1/pages/{slug}/board/batches page board apply
// POST /api/v1/pages/{slug}/board/placements page board pin
// DELETE /api/v1/pages/{slug}/board/placements/{placementId} page board unpin
// GET /api/v1/chats/{chatId}/artifact-results page artifact chat-results

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newPageBoardCommand() *cobra.Command {
	root := &cobra.Command{Use: "board", Short: "Read and update Boards with explicit server revisions"}
	for _, operation := range []struct {
		name, method, short string
		args                int
	}{
		{"show", http.MethodGet, "Read the current Board snapshot", 1},
		{"init", http.MethodPost, "Initialize an existing empty human-owned Page as a Board", 1},
		{"apply", http.MethodPost, "Apply an atomic drawing batch from a JSON file", 1},
		{"pin", http.MethodPost, "Pin an authorized immutable artifact revision", 1},
		{"unpin", http.MethodDelete, "Remove an artifact placement using its current Board sequence", 2},
	} {
		op := operation
		use := op.name + " <slug>"
		if op.args == 2 {
			use += " <placement-id>"
		}
		command := &cobra.Command{Use: use, Short: op.short, Args: cobra.ExactArgs(op.args), RunE: func(cmd *cobra.Command, args []string) error {
			var body any
			if op.method != http.MethodGet {
				if op.name == "init" {
					mutation, _ := cmd.Flags().GetString("mutation-id")
					if strings.TrimSpace(mutation) == "" {
						return fmt.Errorf("--mutation-id is required; reuse it for an identical retry")
					}
					body = map[string]string{"mutation_id": mutation}
				} else {
					file, _ := cmd.Flags().GetString("file")
					if strings.TrimSpace(file) == "" {
						return fmt.Errorf("--file is required; use - for stdin")
					}
					var reader io.Reader = cmd.InOrStdin()
					if file != "-" {
						source, err := os.Open(file)
						if err != nil {
							return err
						}
						defer source.Close()
						reader = source
					}
					raw, err := io.ReadAll(io.LimitReader(reader, (2<<20)+1))
					if err != nil {
						return err
					}
					if len(raw) > 2<<20 {
						return fmt.Errorf("Board mutation exceeds 2 MiB")
					}
					var document map[string]json.RawMessage
					if err := json.Unmarshal(raw, &document); err != nil || document == nil {
						return fmt.Errorf("Board mutation must be a JSON object")
					}
					var mutation string
					if json.Unmarshal(document["mutation_id"], &mutation) != nil || strings.TrimSpace(mutation) == "" {
						return fmt.Errorf("mutation_id is required in the JSON document")
					}
					var sequence int64
					if json.Unmarshal(document["base_seq"], &sequence) != nil || sequence < 0 {
						return fmt.Errorf("base_seq must be a nonnegative integer")
					}
					// Preserve exact input, including duplicate keys, for the server's strict
					// validator. Never silently normalize ambiguous JSON into an accepted write.
					body = bytes.NewReader(raw)
				}
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/board"
			switch op.name {
			case "apply":
				endpoint = "/api/v1/pages/" + pagePathEscape(args[0]) + "/board/batches"
			case "pin":
				endpoint = "/api/v1/pages/" + pagePathEscape(args[0]) + "/board/placements"
			case "unpin":
				endpoint = "/api/v1/pages/" + pagePathEscape(args[0]) + "/board/placements/" + pagePathEscape(args[1])
			}
			response, err := client.Do(op.method, endpoint, body)
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if op.name == "init" {
			command.Flags().String("mutation-id", "", "Required stable identity for this initialization and its retries")
		} else if op.method != http.MethodGet {
			command.Flags().String("file", "", "JSON mutation containing base_seq and mutation_id; - reads stdin")
		}
		root.AddCommand(command)
	}
	root.AddCommand(newPageBoardDelegationCommand(false), newPageBoardDelegationCommand(true), newPageBoardUploadCommand(), newPageBoardDownloadCommand())
	return root
}

func newPageArtifactChatResultsCommand() *cobra.Command {
	return &cobra.Command{Use: "chat-results <chat-id>", Short: "Read artifact results linked by the server to an accessible chat", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/chats/" + pagePathEscape(args[0]) + "/artifact-results")
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
}

func init() { pageCmd.AddCommand(newPageBoardCommand()) }
