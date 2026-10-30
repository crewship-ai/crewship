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
	root := &cobra.Command{Use: "slides", Aliases: []string{"slide"}, Short: "Read or initialize a native schema-1 Slides deck with a human credential", Long: `Read or initialize a native schema-1 Slides deck saved on a private artifact you
can access. Address the artifact by slug or by @<artifact-id>. The server must
start with CREWSHIP_SLIDES_ENABLED=true; otherwise every subcommand returns 404.
These commands refuse agent credentials (CREWSHIP_AGENT_TOKEN).`, Example: `  crewship slides read @<artifact-id>
  crewship slides at-rev @<artifact-id> 1
  crewship slides initialize @<artifact-id> --file initialize.json`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	help := map[string][3]string{
		"read": {"Read the confirmed head of a native Slides deck", `Print the confirmed head: schema_version, revision, order_version,
content_hash and the complete deck. Requires current private read access and,
for a scoped token, pages:read. A missing, inaccessible or uninitialized deck
returns 404 without revealing which.`, `  crewship slides read @<artifact-id>
  crewship slides read <slug> -f yaml`},
		"at-rev": {"Read one exact immutable revision of a native Slides deck", `Print one immutable revision (a positive integer) with the same fields as read.
A revision that does not exist returns 404; the command never substitutes the
latest head.`, `  crewship slides at-rev @<artifact-id> 1`},
		"initialize": {"Save the first revision of a native Slides deck from a JSON request", `Save revision 1 of an empty private artifact that you own. The request carries
page_id, schema_version 1, base_rev 0, a stable mutation_id and a complete deck
whose deck_id equals page_id. Requires pages:write for a scoped token.
Retrying the exact request returns the original receipt with duplicate: true;
changed content under the same mutation_id returns 409.`, `  crewship slides initialize @<artifact-id> --file initialize.json
  cat initialize.json | crewship slides initialize @<artifact-id> --file -`},
	}
	for _, operation := range []string{"read", "at-rev", "initialize"} {
		cmd := &cobra.Command{Use: operation + " <page-slug|@ID>", Args: cobra.ExactArgs(1), Short: help[operation][0], Long: help[operation][1], Example: help[operation][2], RunE: func(cmd *cobra.Command, args []string) error {
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
			cmd.Flags().String("file", "", "Exact schema-1 initialization JSON (required); - reads stdin. Keep page_id and mutation_id unchanged on retry")
		}
		root.AddCommand(cmd)
	}
	return root
}
func init() { rootCmd.AddCommand(newSlideCommand()) }
