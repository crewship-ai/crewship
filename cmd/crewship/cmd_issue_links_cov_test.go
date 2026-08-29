package main

// Coverage for `issue links/link/relink/unlink` (cmd_issue_links.go).
// issueRelinkCmd and issueUnlinkCmd are on the untested-commands list;
// issueLinksCmd/issueLinkCmd have zero coverage too (their names appear
// nowhere else in the suite either) so they're folded in here as the same
// gap under the same file — all four share resolveIssueForLinks and the
// codeLinkItem wire shape.
//
// Fixtures are shaped from codeLinkResponse (internal/api/issue_code_links.go)
// — the handler's own struct — not from the CLI's codeLinkItem decode
// struct, so a field the CLI silently drops or mis-keys would show up as a
// missing assertion rather than a self-confirming round trip.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covLinkID = "clink0123456789abcdefghi"

func covCodeLinkFixture() map[string]any {
	return map[string]any{
		"id": covLinkID, "mission_id": "cissue0123456789abcdefgh", "workspace_id": covWorkspaceIDCli1,
		"provider": "github", "host": "github.com", "owner": "acme", "repo": "thing", "number": 7,
		"kind": "pull_request", "url": "https://github.com/acme/thing/pull/7",
		"title": "Fix the thing", "state": "open", "author": "srba",
		"source_branch": "fix/the-thing", "target_branch": "main",
		"created_at": "2026-06-01T00:00:00Z", "updated_at": "2026-06-01T00:00:00Z",
	}
}

func TestIssueLinksRunE_Happy(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links"
	s.OnGet(path, clitest.JSONResponse(200, []map[string]any{covCodeLinkFixture()}))

	out, err := covCaptureStdout(t, func() error {
		return issueLinksCmd.RunE(issueLinksCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"acme/thing#7", "open", "fix/the-thing", "main"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
}

func TestIssueLinksRunE_StaleAnnotated(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links"
	link := covCodeLinkFixture()
	link["last_sync_error"] = "credential revoked"
	s.OnGet(path, clitest.JSONResponse(200, []map[string]any{link}))

	out, err := covCaptureStdout(t, func() error {
		return issueLinksCmd.RunE(issueLinksCmd, []string{covIssueIdent})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "open (stale)") {
		t.Errorf("stale sync error not annotated in state column:\n%s", out)
	}
}

func TestIssueLinkRunE_Happy(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links"
	s.OnPost(path, clitest.JSONResponse(200, covCodeLinkFixture()))

	out, err := covCaptureStdout(t, func() error {
		return issueLinkCmd.RunE(issueLinkCmd, []string{covIssueIdent, "https://github.com/acme/thing/pull/7"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Linked acme/thing#7 (open) to "+covIssueIdent) {
		t.Errorf("output = %q", out)
	}
	body := covJSONBody(t, s.CallsFor("POST", path)[0].Body)
	if body["url"] != "https://github.com/acme/thing/pull/7" {
		t.Errorf("url = %v", body["url"])
	}
}

func TestIssueLinkRunE_UnsupportedURL(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links"
	// RFC 7807 problem shape — writeCodeLinkProblem's "detail" field, which
	// cli.CheckError also understands.
	s.OnPost(path, clitest.JSONResponse(400, map[string]any{
		"type": "urn:crewship:code-link:unsupported-url", "title": "Bad Request",
		"status": 400, "detail": "unrecognized pull request URL", "code": "unsupported-url",
	}))

	_, err := covCaptureStdout(t, func() error {
		return issueLinkCmd.RunE(issueLinkCmd, []string{covIssueIdent, "https://example.com/not-a-pr"})
	})
	if err == nil || !strings.Contains(err.Error(), "unrecognized pull request URL") {
		t.Errorf("want problem detail surfaced, got %v", err)
	}
}

func TestIssueRelinkRunE_Happy(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links/" + covLinkID + "/refresh"
	refreshed := covCodeLinkFixture()
	refreshed["state"] = "merged"
	s.OnPost(path, clitest.JSONResponse(200, refreshed))

	out, err := covCaptureStdout(t, func() error {
		return issueRelinkCmd.RunE(issueRelinkCmd, []string{covIssueIdent, covLinkID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "acme/thing#7 is merged.") {
		t.Errorf("output = %q", out)
	}
	if n := len(s.CallsFor("POST", path)); n != 1 {
		t.Errorf("POST calls = %d, want 1", n)
	}
}

func TestIssueRelinkRunE_NotFound(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links/ghost-link/refresh"
	s.OnPost(path, clitest.JSONResponse(404, map[string]any{
		"detail": "Code link not found", "code": "link-not-found", "status": 404,
	}))

	_, err := covCaptureStdout(t, func() error {
		return issueRelinkCmd.RunE(issueRelinkCmd, []string{covIssueIdent, "ghost-link"})
	})
	if err == nil || !strings.Contains(err.Error(), "Code link not found") {
		t.Errorf("want not-found error surfaced, got %v", err)
	}
}

func TestIssueUnlinkRunE_Happy(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links/" + covLinkID
	s.OnDelete(path, clitest.JSONResponse(200, map[string]string{"status": "ok"}))

	out, err := covCaptureStdout(t, func() error {
		return issueUnlinkCmd.RunE(issueUnlinkCmd, []string{covIssueIdent, covLinkID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Link removed.") {
		t.Errorf("output = %q", out)
	}
	if n := len(s.CallsFor("DELETE", path)); n != 1 {
		t.Errorf("DELETE calls = %d, want 1", n)
	}
}

func TestIssueUnlinkRunE_NotFound(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	path := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent + "/code-links/ghost-link"
	s.OnDelete(path, clitest.JSONResponse(404, map[string]any{"detail": "Code link not found"}))

	_, err := covCaptureStdout(t, func() error {
		return issueUnlinkCmd.RunE(issueUnlinkCmd, []string{covIssueIdent, "ghost-link"})
	})
	if err == nil || !strings.Contains(err.Error(), "Code link not found") {
		t.Errorf("want not-found error surfaced, got %v", err)
	}
}

// issueLinksCmd et al. share resolveIssueForLinks, which fails closed when
// the issue itself doesn't resolve — before any code-links call is made.
func TestIssueLinksRunE_IssueNotFound(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/issues/GHOST-1", clitest.ErrorResponse(404, "issue not found"))

	_, err := covCaptureStdout(t, func() error {
		return issueLinksCmd.RunE(issueLinksCmd, []string{"GHOST-1"})
	})
	if err == nil || !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("want issue-not-found error, got %v", err)
	}
	if calls := s.CallsFor("GET", "/api/v1/crews/"+covCrewID+"/issues/GHOST-1/code-links"); len(calls) != 0 {
		t.Errorf("code-links must not be called when the issue itself doesn't resolve")
	}
}
