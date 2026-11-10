package main

// GET /api/v1/pages/{slug}/document/proposals doc proposals
// GET /api/v1/pages/{slug}/document/proposals/{proposal} doc proposal
// POST /api/v1/pages/{slug}/document/proposals/{proposal}/accept doc proposal-accept
// POST /api/v1/pages/{slug}/document/proposals/{proposal}/reject doc proposal-reject

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/spf13/cobra"
)

var docProposalRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func newDocProposalCommands() []*cobra.Command {
	list := &cobra.Command{Use: "proposals <page-slug>", Short: "List agent proposals on a schema 4 or 5 document", Long: `Print proposals (oldest first) with id, author, run_id, target.block_ids,
base_rev, status (open, stale, accepted or rejected), text and created_at.
An open proposal turns stale when its block changed after it was made; it can
then no longer be accepted. Reading needs read access to the document.`, Example: `  crewship doc proposals @<artifact-id>`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/document/proposals")
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	show := &cobra.Command{Use: "proposal <page-slug> <proposal-id>", Short: "Show one proposal with its block before and after", Long: `Print one proposal with base_content (the block as the agent saw it) and
proposed_content (the block after the change), both as document JSON.`, Example: `  crewship doc proposal @<artifact-id> <proposal-id>`, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/document/proposals/" + url.PathEscape(args[1]))
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	decide := func(verb, short, long string) *cobra.Command {
		cmd := &cobra.Command{Use: "proposal-" + verb + " <page-slug> <proposal-id>", Short: short, Long: long, Example: fmt.Sprintf("  crewship doc proposal-%s @<artifact-id> <proposal-id> --expected-rev 7 --request-id decide-1", verb), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			rev, _ := cmd.Flags().GetInt64("expected-rev")
			request, _ := cmd.Flags().GetString("request-id")
			if !cmd.Flags().Changed("expected-rev") || rev < 0 {
				return fmt.Errorf("--expected-rev is required: the document rev you reviewed (crewship doc read)")
			}
			if !docProposalRequestID.MatchString(request) {
				return fmt.Errorf("--request-id is required (1-128 letters, digits, - or _); reuse it verbatim to retry an uncertain decision")
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			body := map[string]any{"request_id": request, "expected_rev": rev}
			base := "/api/v1/pages/" + pagePathEscape(args[0]) + "/document/proposals/" + url.PathEscape(args[1])
			var response *http.Response
			if verb == "accept" {
				response, err = client.Post(base+"/accept", body)
			} else {
				response, err = client.Post(base+"/reject", body)
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		cmd.Flags().Int64("expected-rev", -1, "Document revision you reviewed; a newer head refuses the decision with 409")
		cmd.Flags().String("request-id", "", "Idempotency key for this one decision; reuse it verbatim on retry")
		return cmd
	}
	accept := decide("accept", "Accept a proposal: apply exactly its change as a new revision", `Apply the proposal as a new document revision made by you. It succeeds only
if the document is still at --expected-rev and the proposed block is unchanged
since the proposal was made; otherwise 409 and nothing changes. You need Edit
access. A retry with the same --request-id returns the original receipt.`)
	reject := decide("reject", "Reject a proposal; the document is unchanged", `Close an open proposal without changing the document. You need Edit access.
A retry with the same --request-id returns the original receipt.`)
	return []*cobra.Command{list, show, accept, reject}
}
