package main

// Coverage for PRD-ISSUES-AND-ROUTINES-2026 work package B7 ("Hard
// termination (Tier 2)", #2356): `crewship issue stop --hard` sends the
// Tier 2 opt-in query param, and plain `stop` (no flag) does not.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestIssueStopRunE_Hard(t *testing.T) {
	guardCLIState(t)

	t.Run("--hard sends hard=true", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-30")
		stopPath := "/api/v1/crews/" + covCrewIDCli7 + "/issues/ENG-30/stop"
		s.OnPost(stopPath, clitest.JSONResponse(200, map[string]any{
			"status": "CANCELLED", "identifier": "ENG-30", "runs_stopped": 1, "hard": true,
		}))
		if err := issueStopCmd.Flags().Set("hard", "true"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = issueStopCmd.Flags().Set("hard", "false") })

		if err := issueStopCmd.RunE(issueStopCmd, []string{"ENG-30"}); err != nil {
			t.Fatalf("stop --hard: %v", err)
		}
		posts := s.CallsFor("POST", stopPath)
		if len(posts) != 1 {
			t.Fatalf("stop POSTs = %d, want 1", len(posts))
		}
		if !strings.Contains(posts[0].Query, "hard=true") {
			t.Errorf("query = %q, want it to contain hard=true", posts[0].Query)
		}
	})

	t.Run("without --hard sends no query", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-31")
		stopPath := "/api/v1/crews/" + covCrewIDCli7 + "/issues/ENG-31/stop"
		s.OnPost(stopPath, clitest.JSONResponse(200, map[string]any{
			"status": "CANCELLED", "identifier": "ENG-31", "runs_stopped": 1, "hard": false,
		}))
		_ = issueStopCmd.Flags().Set("hard", "false")

		if err := issueStopCmd.RunE(issueStopCmd, []string{"ENG-31"}); err != nil {
			t.Fatalf("stop: %v", err)
		}
		posts := s.CallsFor("POST", stopPath)
		if len(posts) != 1 {
			t.Fatalf("stop POSTs = %d, want 1", len(posts))
		}
		if strings.Contains(posts[0].Query, "hard=true") {
			t.Errorf("query = %q, must not opt into Tier 2 without --hard", posts[0].Query)
		}
	})
}
