package main

// Coverage tests for cmd_issue_workflow.go — comment / labels / start /
// stop / review.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covIssueStub registers the GET /api/v1/issues/{ident} lookup that the
// workflow commands use to discover crew_id + canonical identifier.
func covIssueStub(s *clitest.StubServer, ident string) {
	s.OnGet("/api/v1/issues/"+ident, clitest.JSONResponse(200, map[string]any{
		"id":         "ciss1234567890123456789",
		"crew_id":    covCrewIDCli7,
		"identifier": ident,
		"title":      "Fix the bilge pump",
		"status":     "TODO",
		"priority":   "high",
	}))
}

func TestIssueWorkflow_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"comment", issueCommentCmd, []string{"ENG-1", "hello"}},
		{"labels", issueLabelsCmd, nil},
		{"start", issueStartCmd, []string{"ENG-1"}},
		{"stop", issueStopCmd, []string{"ENG-1"}},
		{"review", issueReviewCmd, []string{"ENG-1"}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", tc.name, err)
		}
	}
}

func TestIssueCommentRunE(t *testing.T) {
	commentPath := "/api/v1/crews/" + covCrewIDCli7 + "/issues/ENG-1/comments"

	t.Run("body from positional args", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-1")
		s.OnPost(commentPath, clitest.JSONResponse(201, map[string]string{"id": "cm-1"}))
		_ = issueCommentCmd.Flags().Set("body", "")

		if err := issueCommentCmd.RunE(issueCommentCmd, []string{"ENG-1", "looks", "good"}); err != nil {
			t.Fatalf("comment: %v", err)
		}
		posts := s.CallsFor("POST", commentPath)
		if len(posts) != 1 {
			t.Fatalf("expected 1 POST, got %d", len(posts))
		}
		var body map[string]string
		_ = json.Unmarshal(posts[0].Body, &body)
		if body["body"] != "looks good" {
			t.Errorf("comment body = %q", body["body"])
		}
	})

	t.Run("--body flag wins over args", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-1")
		s.OnPost(commentPath, clitest.JSONResponse(201, map[string]string{"id": "cm-2"}))
		if err := issueCommentCmd.Flags().Set("body", "from flag"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = issueCommentCmd.Flags().Set("body", "") })

		if err := issueCommentCmd.RunE(issueCommentCmd, []string{"ENG-1", "ignored"}); err != nil {
			t.Fatalf("comment: %v", err)
		}
		posts := s.CallsFor("POST", commentPath)
		var body map[string]string
		_ = json.Unmarshal(posts[len(posts)-1].Body, &body)
		if body["body"] != "from flag" {
			t.Errorf("comment body = %q, want flag value", body["body"])
		}
	})

	t.Run("missing body", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		_ = issueCommentCmd.Flags().Set("body", "")
		err := issueCommentCmd.RunE(issueCommentCmd, []string{"ENG-1"})
		if err == nil || !strings.Contains(err.Error(), "comment body is required") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("issue lookup failure", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		s.OnGet("/api/v1/issues/GHOST-1", clitest.ErrorResponse(404, "issue not found"))
		err := issueCommentCmd.RunE(issueCommentCmd, []string{"GHOST-1", "hi"})
		if err == nil || !strings.Contains(err.Error(), "issue not found") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestIssueLabelsRunE(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	group := "area"
	s.OnGet("/api/v1/labels", clitest.JSONResponse(200, []map[string]any{
		{"id": "l1", "name": "bug", "color": "#ff0000", "group": group},
		{"id": "l2", "name": "feature", "color": "#00ff00"},
	}))

	out, err := covCaptureStdoutCli7(t, func() error {
		return issueLabelsCmd.RunE(issueLabelsCmd, nil)
	})
	if err != nil {
		t.Fatalf("labels: %v", err)
	}
	for _, want := range []string{"bug", "#ff0000", "area", "feature"} {
		if !strings.Contains(out, want) {
			t.Errorf("labels table missing %q:\n%s", want, out)
		}
	}
}

func TestIssueStartStopRunE(t *testing.T) {
	t.Run("start dispatches", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-2")
		startPath := "/api/v1/crews/" + covCrewIDCli7 + "/issues/ENG-2/start"
		s.OnPost(startPath, clitest.JSONResponse(200, map[string]string{}))

		if err := issueStartCmd.RunE(issueStartCmd, []string{"ENG-2"}); err != nil {
			t.Fatalf("start: %v", err)
		}
		if n := len(s.CallsFor("POST", startPath)); n != 1 {
			t.Errorf("start POSTs = %d", n)
		}
	})

	t.Run("start rejection surfaces", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-2")
		s.OnPost("/api/v1/crews/"+covCrewIDCli7+"/issues/ENG-2/start", clitest.ErrorResponse(409, "no assignee"))
		err := issueStartCmd.RunE(issueStartCmd, []string{"ENG-2"})
		if err == nil || !strings.Contains(err.Error(), "no assignee") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("stop cancels", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-3")
		stopPath := "/api/v1/crews/" + covCrewIDCli7 + "/issues/ENG-3/stop"
		s.OnPost(stopPath, clitest.JSONResponse(200, map[string]string{}))

		if err := issueStopCmd.RunE(issueStopCmd, []string{"ENG-3"}); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if n := len(s.CallsFor("POST", stopPath)); n != 1 {
			t.Errorf("stop POSTs = %d", n)
		}
	})
}

func TestIssueReviewRunE(t *testing.T) {
	reviewPath := "/api/v1/crews/" + covCrewIDCli7 + "/issues/ENG-4/review"

	resetReviewFlags := func(t *testing.T) {
		t.Helper()
		t.Cleanup(func() {
			_ = issueReviewCmd.Flags().Set("action", "")
			_ = issueReviewCmd.Flags().Set("comment", "")
			_ = issueReviewCmd.Flags().Set("reassign", "")
		})
	}

	t.Run("action required", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-4")
		resetReviewFlags(t)
		_ = issueReviewCmd.Flags().Set("action", "")
		err := issueReviewCmd.RunE(issueReviewCmd, []string{"ENG-4"})
		if err == nil || !strings.Contains(err.Error(), "--action is required") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("invalid action", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-4")
		resetReviewFlags(t)
		_ = issueReviewCmd.Flags().Set("action", "merge")
		err := issueReviewCmd.RunE(issueReviewCmd, []string{"ENG-4"})
		if err == nil || !strings.Contains(err.Error(), "must be 'approve' or 'request_changes'") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("approve with comment", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-4")
		s.OnPost(reviewPath, clitest.JSONResponse(200, map[string]string{}))
		resetReviewFlags(t)
		_ = issueReviewCmd.Flags().Set("action", "approve")
		_ = issueReviewCmd.Flags().Set("comment", "ship it")

		if err := issueReviewCmd.RunE(issueReviewCmd, []string{"ENG-4"}); err != nil {
			t.Fatalf("review approve: %v", err)
		}
		posts := s.CallsFor("POST", reviewPath)
		if len(posts) != 1 {
			t.Fatalf("review POSTs = %d", len(posts))
		}
		var body map[string]any
		_ = json.Unmarshal(posts[0].Body, &body)
		if body["action"] != "approve" || body["comment"] != "ship it" {
			t.Errorf("review body = %v", body)
		}
		if _, has := body["reassign_to"]; has {
			t.Errorf("reassign_to must be omitted when unset: %v", body)
		}
	})

	t.Run("request_changes with reassign", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-4")
		s.OnPost(reviewPath, clitest.JSONResponse(200, map[string]string{}))
		resetReviewFlags(t)
		_ = issueReviewCmd.Flags().Set("action", "request_changes")
		_ = issueReviewCmd.Flags().Set("reassign", "eva")

		if err := issueReviewCmd.RunE(issueReviewCmd, []string{"ENG-4"}); err != nil {
			t.Fatalf("review request_changes: %v", err)
		}
		posts := s.CallsFor("POST", reviewPath)
		var body map[string]any
		_ = json.Unmarshal(posts[len(posts)-1].Body, &body)
		if body["action"] != "request_changes" || body["reassign_to"] != "eva" {
			t.Errorf("review body = %v", body)
		}
	})
}

// ─── additional error paths ──────────────────────────────────────────────

func TestIssueWorkflow_NoWorkspace(t *testing.T) {
	covNoWorkspaceCLI(t)

	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"comment", issueCommentCmd, []string{"ENG-1", "hello"}},
		{"labels", issueLabelsCmd, nil},
		{"start", issueStartCmd, []string{"ENG-1"}},
		{"stop", issueStopCmd, []string{"ENG-1"}},
		{"review", issueReviewCmd, []string{"ENG-1"}},
	}
	for _, tc := range cases {
		if err := tc.cmd.RunE(tc.cmd, tc.args); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error, got %v", tc.name, err)
		}
	}
}

func TestIssueLabelsRunE_Errors(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		saveCLIState(t)
		flagServer = ""
		flagWorkspace = ""
		cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWSCli7, Server: covDeadURL(t)}
		if err := issueLabelsCmd.RunE(issueLabelsCmd, nil); err == nil || !strings.Contains(err.Error(), "request failed") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("API error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		s.OnGet("/api/v1/labels", clitest.ErrorResponse(500, "db down"))
		err := issueLabelsCmd.RunE(issueLabelsCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "db down") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("undecodable body", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		s.OnGet("/api/v1/labels", clitest.TextResponse(200, "x"))
		if err := issueLabelsCmd.RunE(issueLabelsCmd, nil); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestIssueCommentRunE_PostRejected(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covIssueStub(s, "ENG-1")
	s.OnPost("/api/v1/crews/"+covCrewIDCli7+"/issues/ENG-1/comments", clitest.ErrorResponse(403, "comments locked"))
	_ = issueCommentCmd.Flags().Set("body", "")

	err := issueCommentCmd.RunE(issueCommentCmd, []string{"ENG-1", "hi"})
	if err == nil || !strings.Contains(err.Error(), "comments locked") {
		t.Fatalf("got %v", err)
	}
}

func TestIssueStartStopReview_FetchIssueErrors(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	s.OnGet("/api/v1/issues/GHOST-9", clitest.ErrorResponse(404, "issue not found"))

	if err := issueStartCmd.RunE(issueStartCmd, []string{"GHOST-9"}); err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("start: %v", err)
	}
	if err := issueStopCmd.RunE(issueStopCmd, []string{"GHOST-9"}); err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("stop: %v", err)
	}
	_ = issueReviewCmd.Flags().Set("action", "approve")
	t.Cleanup(func() { _ = issueReviewCmd.Flags().Set("action", "") })
	if err := issueReviewCmd.RunE(issueReviewCmd, []string{"GHOST-9"}); err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("review: %v", err)
	}
}

func TestIssueStopReview_PostRejected(t *testing.T) {
	t.Run("stop rejection", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-3")
		s.OnPost("/api/v1/crews/"+covCrewIDCli7+"/issues/ENG-3/stop", clitest.ErrorResponse(409, "nothing running"))
		err := issueStopCmd.RunE(issueStopCmd, []string{"ENG-3"})
		if err == nil || !strings.Contains(err.Error(), "nothing running") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("review rejection", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covIssueStub(s, "ENG-4")
		s.OnPost("/api/v1/crews/"+covCrewIDCli7+"/issues/ENG-4/review", clitest.ErrorResponse(409, "not in review"))
		_ = issueReviewCmd.Flags().Set("action", "approve")
		t.Cleanup(func() { _ = issueReviewCmd.Flags().Set("action", "") })
		err := issueReviewCmd.RunE(issueReviewCmd, []string{"ENG-4"})
		if err == nil || !strings.Contains(err.Error(), "not in review") {
			t.Fatalf("got %v", err)
		}
	})
}
