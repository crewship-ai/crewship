package main

// GET /api/v1/pages/{slug}/slides slide read
// GET /api/v1/pages/{slug}/slides/outline slide outline
// GET /api/v1/pages/{slug}/slides/history slide history
// GET /api/v1/pages/{slug}/slides/revisions/{revision} slide at-rev
// POST /api/v1/pages/{slug}/slides/initialize slide initialize
// POST /api/v1/pages/{slug}/slides/patch slide patch
// POST /api/v1/pages/{slug}/slides/reorder slide reorder
// POST /api/v1/pages/{slug}/slides/restore slide restore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/slides"
)

func newSlideCommand() *cobra.Command {
	root := &cobra.Command{Use: "slide", Short: "Read and mutate typed Page Slides with immutable preconditions"}
	for _, operation := range []string{"read", "outline", "history", "at-rev", "initialize", "patch", "reorder", "restore"} {
		cmd := &cobra.Command{Use: operation + " <page-slug>", Short: "Slides " + operation, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			client, err := pageClient()
			if err != nil {
				return err
			}
			path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/slides"
			var response *http.Response
			switch operation {
			case "read":
				response, err = client.Get(path)
			case "outline":
				response, err = client.Get(path + "/outline")
			case "history":
				response, err = client.Get(path + "/history")
			case "at-rev":
				revision, parseErr := strconv.ParseInt(args[1], 10, 64)
				if parseErr != nil || revision < 1 {
					return fmt.Errorf("positive revision required")
				}
				response, err = client.Get(path + "/revisions/" + args[1])
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
				raw, readErr := io.ReadAll(io.LimitReader(reader, slides.MaxDeckBytes+1))
				if readErr != nil {
					return readErr
				}
				// The CLI validates the same exact bounded contract before transport. It
				// never fetches/rebases Page identity, revision, order or object hashes.
				switch operation {
				case "initialize":
					_, err = slides.DecodeInitialize(raw)
				case "patch":
					_, err = slides.DecodePatch(raw)
				case "reorder":
					_, err = slides.DecodeReorder(raw)
				case "restore":
					_, err = slides.DecodeRestore(raw)
				}
				if err != nil {
					return err
				}
				switch operation {
				case "initialize":
					response, err = client.Post(path+"/initialize", json.RawMessage(raw))
				case "patch":
					response, err = client.Post(path+"/patch", json.RawMessage(raw))
				case "reorder":
					response, err = client.Post(path+"/reorder", json.RawMessage(raw))
				case "restore":
					response, err = client.Post(path+"/restore", json.RawMessage(raw))
				default:
					return fmt.Errorf("unsupported Slides operation")
				}
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if operation == "at-rev" {
			cmd.Use = "at-rev <page-slug> <revision>"
			cmd.Args = cobra.ExactArgs(2)
		}
		if operation == "initialize" || operation == "patch" || operation == "reorder" || operation == "restore" {
			cmd.Flags().String("file", "", "Exact JSON request; - reads stdin. Preserve page_id, mutation_id and all preconditions on retry")
		}
		root.AddCommand(cmd)
	}
	return root
}
func init() { rootCmd.AddCommand(newSlideCommand()) }
