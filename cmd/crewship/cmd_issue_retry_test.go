package main

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func TestIssueCLIForwardsRevisionAndRetryKeys(t *testing.T) {
	stub := covSetupCli4(t)
	stubIssueDirectory(stub)
	path := "/api/v1/crews/" + covCrewIDCli4 + "/issues/ENG-7"
	stub.OnPatch(path, clitest.JSONResponse(200, map[string]any{"id": "issue", "identifier": "ENG-7", "title": "New"}))
	update := covFreshCmd(issueUpdateCmd, func(c *cobra.Command) {
		declareIssueUpdateFlags(c)
		c.Flags().Int64("expected-revision", 0, "")
		c.Flags().String("idempotency-key", "", "")
	})
	covSetFlagsCli4(t, update, map[string]string{"title": "New", "expected-revision": "7", "idempotency-key": "patch-7"})
	if err := update.RunE(update, []string{"ENG-7"}); err != nil {
		t.Fatal(err)
	}
	calls := stub.CallsFor("PATCH", path)
	if len(calls) != 1 || calls[0].Headers.Get("Idempotency-Key") != "patch-7" {
		t.Fatalf("patch key missing: %+v", calls)
	}
	var patch map[string]any
	if err := json.Unmarshal(calls[0].Body, &patch); err != nil {
		t.Fatal(err)
	}
	if patch["expected_revision"] != float64(7) {
		t.Fatalf("revision missing: %v", patch)
	}
	stub.OnPost(path+"/comments", clitest.JSONResponse(201, map[string]any{"id": "comment", "body": "Once"}))
	comment := covFreshCmd(issueCommentCmd, func(c *cobra.Command) {
		c.Flags().String("body", "", "")
		c.Flags().StringArray("mention", nil, "")
		c.Flags().String("idempotency-key", "", "")
	})
	covSetFlagsCli4(t, comment, map[string]string{"body": "Once", "idempotency-key": "comment-7"})
	if err := comment.RunE(comment, []string{"ENG-7"}); err != nil {
		t.Fatal(err)
	}
	calls = stub.CallsFor("POST", path+"/comments")
	if len(calls) != 1 || calls[0].Headers.Get("Idempotency-Key") != "comment-7" {
		t.Fatalf("comment key missing: %+v", calls)
	}
}
