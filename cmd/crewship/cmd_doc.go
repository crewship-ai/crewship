package main

// GET /api/v1/documents/status doc status
// POST /api/v1/pages/{slug}/document/initialize doc initialize
// GET /api/v1/pages/{slug}/document doc read, doc outline
// POST /api/v1/pages/{slug}/document/patch doc patch
// POST /api/v1/pages/{slug}/document/upgrade doc upgrade
// GET /api/v1/pages/{slug}/document/comments doc comments
// POST /api/v1/pages/{slug}/document/comments doc comment
// POST /api/v1/pages/{slug}/document/comments/resolve doc comment-resolve
// GET /api/v1/pages/{slug}/document/people doc people

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

func newDocCommand() *cobra.Command {
	root := &cobra.Command{Use: "doc", Short: "Read and edit native documents with revision and hash checks", Long: `Read and edit native documents (shown as Word in the Artifacts library).
Address a document by its artifact slug or by @<artifact-id>. The server must
start with CREWSHIP_DOCUMENTS_ENABLED=true; check with crewship doc status.
Scoped tokens need pages:read to read and pages:read plus pages:write to edit.
create and draft are for an admitted agent run only.`, Example: `  crewship doc status
  crewship doc read @<artifact-id>
  crewship doc patch @<artifact-id> --file patch.json`}
	help := map[string][3]string{
		"read": {"Read the confirmed document, outline, revision and epoch", `Print the confirmed content tree, block outline, rev, schema, epoch, page_id
and the caller's can_write, can_comment and can_resolve. Use epoch, rev and a
block hash from this output when preparing a patch.`, `  crewship doc read @<artifact-id>
  crewship doc read <slug> -f yaml`},
		"outline": {"Print the current block IDs, types, hashes and text as JSON", `Print a JSON array of {id, type, parent, hash, text} for the confirmed blocks;
parent is null at the top level. Reads the same state as read and never
changes the document.`, `  crewship doc outline @<artifact-id>`},
		"patch": {"Apply a guarded text replacement to one block", `Send one JSON object (at most 2 MiB) with id, schema, epoch, target, text, ifRev
and ifHash; schemas 3 to 6 also need page_id. A changed revision, hash or epoch
refuses the edit and nothing is saved. Resend the exact request with the same id
after a lost confirmation; the original receipt comes back with found: true
and duplicate: true. A stale epoch, revision or hash returns 409.`, `  crewship doc patch @<artifact-id> --file patch.json
  cat patch.json | crewship doc patch @<artifact-id> --file -`},
		"status": {"Show whether native documents are enabled on this server", `Print enabled, wordcraft_native and gridcraft_native for the selected server.
enabled is false unless the server starts with CREWSHIP_DOCUMENTS_ENABLED=true.`, `  crewship doc status
  crewship doc status -f yaml`},
		"initialize": {"Write the first content of an empty artifact as a document", `Send one JSON object (at most 2 MiB) with an operation id, schema (2 to 6)
and a body document tree; schemas 3 to 6 also need page_id. Schema 4 adds
bold, italic and strike marks, code block languages, quotes and checklists;
schema 5 adds underline and palette text colour and highlight; schema 6 adds
the Canvas view and starts with an empty canvas. Resend the exact request
after a lost confirmation; the original receipt comes back with duplicate: true.
The artifact must be empty and owned by you, not by a crew (for example one made
with crewship page create --allow-empty). Read the document afterwards to obtain
its epoch.`, `  crewship doc initialize @<artifact-id> --file initialize.json`},
		"upgrade": {"Upgrade a document to the newest schema, for example to enable Canvas", `Send one JSON object with id, schema (the document's current schema),
target_schema (6, the newest), page_id, epoch and ifRev from doc read. The
upgrade only widens what the document may hold: its text, block IDs and hashes,
versions and comments stay as they are, and schema 6 adds the Canvas view.
Open proposals can afterwards only be rejected; an agent proposes again. You need Edit access; agents cannot upgrade. A changed revision or
epoch returns 409 and nothing changes. Resend the exact request after a lost
confirmation; the original receipt comes back with duplicate: true. Editors
still open at the old schema must reload before they can save again.`, `  crewship doc upgrade @<artifact-id> --file upgrade.json`},
		"comments": {"List the document's comment threads and replies", `Print comments (oldest first) with id, body, actor, created_at, version and
resolved, plus can_comment and can_resolve for you. A thread has its quoted
text, block and status (attached, collapsed, detached or resolved); a reply has
parent, the thread it answers. Reading needs read access to the document.`, `  crewship doc comments @<artifact-id>
  crewship doc comments <slug> -f yaml`},
		"comment": {"Start a comment thread on quoted text, or reply to one", `Send one JSON object with id, schema, epoch, ifRev and text, plus page_id for
schemas 3 to 6. A new thread also names block, from and to: the UTF-16 range
of the quoted text inside one paragraph, heading or code block (see doc outline).
A reply names parent, the thread's id, instead. Comments never change content.
You need Comment or Edit access. Resend the exact request after a lost
confirmation; a changed revision returns 409.`, `  crewship doc comment @<artifact-id> --file comment.json`},
		"comment-resolve": {"Resolve or reopen a comment thread", `Send one JSON object with id, schema, epoch, ifRev, page_id (schemas 3 to 6),
comment (the thread id), version (from doc comments) and resolved (true or
false). A changed version or revision returns 409. You need Comment or Edit
access.`, `  crewship doc comment-resolve @<artifact-id> --file resolve.json`},
	}
	for _, operation := range []string{"read", "outline", "patch", "status", "initialize", "upgrade", "comments", "comment", "comment-resolve"} {
		operation := operation
		cmd := &cobra.Command{Use: operation + " <page-slug>", Short: help[operation][0], Long: help[operation][1], Example: help[operation][2], Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
			case "comments":
				response, err = client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/document/comments")
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
				case "upgrade":
					response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/upgrade", body)
				case "comment":
					response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/comments", body)
				case "comment-resolve":
					response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/comments/resolve", body)
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
		if operation == "patch" || operation == "initialize" || operation == "upgrade" || operation == "comment" || operation == "comment-resolve" {
			cmd.Flags().String("file", "", "JSON request file; - reads stdin. Preserve operation IDs when retrying an exact request")
		}
		root.AddCommand(cmd)
	}
	root.AddCommand(newDocCreateCommand())
	root.AddCommand(newDocDraftCommand())
	root.AddCommand(newDocResultsCommand())
	root.AddCommand(newDocHistoryCommands()...)
	root.AddCommand(newDocProposalCommands()...)
	root.AddCommand(newDocPeopleCommand())
	return root
}

// newDocPeopleCommand wraps GET /api/v1/pages/{slug}/document/people (#5155).
func newDocPeopleCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "people <page-slug>", Short: "Name mentioned people, or search people to mention, for this reader",
		Long: `Mentions in a schema 5 document store only a user id. With --ids, print the
names of those people as {"people":[{id,name}]}; with --query (or neither
flag), list at most 20 people whose name contains the text. Only people who can
read this document appear, and only when you can read it. Any other id is simply
absent, which a reader shows as Unknown member; the answer never says whether
an id exists. Reading needs read access to the document.`,
		Example: `  crewship doc people @<artifact-id> --ids <user-id>,<user-id>
  crewship doc people <slug> --query ada`,
		Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			ids, _ := cmd.Flags().GetString("ids")
			search, _ := cmd.Flags().GetString("query")
			if ids != "" && search != "" {
				return fmt.Errorf("use either --ids or --query")
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			query := url.Values{}
			if ids != "" {
				query.Set("ids", ids)
			} else {
				query.Set("q", search)
			}
			response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/document/people?" + query.Encode())
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
	cmd.Flags().String("ids", "", "Comma-separated mention user ids to name (at most 100)")
	cmd.Flags().String("query", "", "Name text to search for people who can read this document")
	return cmd
}

// Re-encode JSON even for human output: untrusted source stays escaped text,
// including terminal control characters, rather than executing or opening HTML.
func printPageArtifactResponse(cmd *cobra.Command, response *http.Response) error {
	defer response.Body.Close()
	if err := pageCheckError(response); err != nil {
		return err
	}
	const limit = 64 << 20 // JSON escaping can expand the 10 MiB source sixfold.
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if len(data) > limit {
		return fmt.Errorf("artifact response exceeds limit")
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	safe, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var writeErr error
	if err := resolvedFormatter(cmd).AutoHuman(payload, func() { _, writeErr = fmt.Fprintln(cmd.OutOrStdout(), string(safe)) }); err != nil {
		return err
	}
	return writeErr
}

func init() { rootCmd.AddCommand(newDocCommand()) }
