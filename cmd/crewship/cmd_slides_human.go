package main

// GET /api/v1/pages/{slug}/slides slides read
// GET /api/v1/pages/{slug}/slides/revisions/{revision} slides at-rev
// POST /api/v1/pages/{slug}/slides/initialize slides initialize

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

// Retains the native CLI's immutable request contract; no automatic Page lookup,
// precondition refresh, broad write operations or agent-profile fallback.
func newSlideCommand() *cobra.Command {
	root := &cobra.Command{Use: "slides", Aliases: []string{"slide"}, Short: "Read or initialize saved schema-1 Slides on a private Page", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	for _, operation := range []string{"read", "at-rev", "initialize"} {
		cmd := &cobra.Command{Use: operation + " <page-slug|@ID>", Args: cobra.ExactArgs(1), Short: "Slides " + operation, RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
				return fmt.Errorf("human Slides commands require a human credential")
			}
			basePath := "/api/v1/pages/" + pagePathEscape(args[0]) + "/slides"
			readPath := basePath
			if operation == "at-rev" {
				n, e := strconv.ParseInt(args[1], 10, 64)
				if e != nil || n < 1 {
					return fmt.Errorf("positive revision required")
				}
				readPath += "/revisions/" + args[1]
			}
			var raw []byte
			if operation == "initialize" {
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
				var e error
				raw, e = io.ReadAll(io.LimitReader(reader, slides.MaxDeckBytes+1))
				if e != nil {
					return e
				}
				request, e := slides.DecodeInitialize(raw)
				if e != nil {
					return e
				}
				if request.SchemaVersion != slides.SchemaVersion || request.Deck.SchemaVersion != slides.SchemaVersion {
					return fmt.Errorf("only Slides schema 1 is available")
				}
			}
			client, e := pageClient()
			if e != nil {
				return e
			}
			var response *http.Response
			if operation == "initialize" {
				response, e = client.Post(basePath+"/initialize", json.RawMessage(raw))
			} else {
				response, e = client.Get(readPath)
			}
			if e != nil {
				return e
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if operation == "at-rev" {
			cmd.Use = "at-rev <page-slug|@ID> <revision>"
			cmd.Args = cobra.ExactArgs(2)
		}
		if operation == "initialize" {
			cmd.Flags().String("file", "", "Exact schema-1 initialization JSON; - reads stdin. Retain Page and mutation IDs on retry")
		}
		root.AddCommand(cmd)
	}
	return root
}
func init() { rootCmd.AddCommand(newSlideCommand()) }
