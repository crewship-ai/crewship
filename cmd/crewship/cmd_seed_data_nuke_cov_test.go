package main

// Coverage tests for cmd_seed_data_nuke.go. Also hosts small shared
// helpers (covWSCli7 / covStubClient / covSetupCLI / covCaptureStdoutCli7) reused
// by the other *_cov_test.go files in this package.

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covWSCli7 is a CUID-shaped workspace id (>=21 chars, leading 'c', lowercase
// alnum) so neither the cmd-level nor the client-level looksLikeCUID
// helper triggers a slug-resolution round-trip.
const covWSCli7 = "cws0123456789abcdefghijk"

// covStubClient returns a cli.Client wired at the stub server with the
// canonical test token + workspace.
func covStubClient(s *clitest.StubServer) *cli.Client {
	return cli.NewClient(s.URL(), "test-token", covWSCli7)
}

// covSetupCLI points the package-level CLI globals at the stub server.
// NOT parallel-safe — callers must not use t.Parallel().
func covSetupCLI(t *testing.T, s *clitest.StubServer) {
	t.Helper()
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWSCli7, Server: s.URL()}
}

// covCaptureStdoutCli7 runs fn with os.Stdout redirected to a pipe and
// returns whatever was printed plus fn's error.
func covCaptureStdoutCli7(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	data, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}
	return string(data), runErr
}

// covDeadURL returns the URL of a stub server that has already been
// closed — every request to it fails at the transport layer.
func covDeadURL(t *testing.T) string {
	t.Helper()
	s := clitest.NewStubServer()
	url := s.URL()
	s.Close()
	return url
}

// covDeadClient returns a client whose requests all fail with a
// connection error.
func covDeadClient(t *testing.T) *cli.Client {
	t.Helper()
	return cli.NewClient(covDeadURL(t), "test-token", covWSCli7)
}

// covNoWorkspaceCLI configures globals with auth but no workspace.
func covNoWorkspaceCLI(t *testing.T) {
	t.Helper()
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	cliCfg = &cli.CLIConfig{Token: "test-token"}
}

// ─── confirmNuke ─────────────────────────────────────────────────────────

func newYesCmd(yes bool) *cobra.Command {
	c := &cobra.Command{Use: "x"}
	c.Flags().Bool("yes", yes, "")
	return c
}

func TestConfirmNuke_YesBypassesPrompt(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covWSCli7, "name": "Demo", "slug": "demo"},
	}))
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{{"id": "c1"}}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{{"id": "a1"}, {"id": "a2"}}))

	if err := confirmNuke(newYesCmd(true), covStubClient(s), s.URL()); err != nil {
		t.Fatalf("confirmNuke with --yes: %v", err)
	}
}

func TestConfirmNuke_NonInteractiveRefuses(t *testing.T) {
	// In `go test`, stdin/stdout are not TTYs, so the interactive branch is
	// unreachable and the gate must fail closed.
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covWSCli7, "name": "Demo", "slug": "demo"},
	}))
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	err := confirmNuke(newYesCmd(false), covStubClient(s), s.URL())
	if err == nil || !strings.Contains(err.Error(), "refusing to nuke") {
		t.Fatalf("expected 'refusing to nuke' error, got %v", err)
	}
}

// ─── nukeWorkspaceIdentity ───────────────────────────────────────────────

func TestNukeWorkspaceIdentity(t *testing.T) {
	t.Run("active workspace found", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
			{"id": "cother123456789012345678", "name": "Other", "slug": "other"},
			{"id": covWSCli7, "name": "Demo WS", "slug": "demo-ws"},
		}))
		name, slug := nukeWorkspaceIdentity(covStubClient(s))
		if name != "Demo WS" || slug != "demo-ws" {
			t.Errorf("got (%q,%q), want (Demo WS, demo-ws)", name, slug)
		}
	})

	t.Run("no match fails closed", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
			{"id": "cother123456789012345678", "name": "Other", "slug": "other"},
		}))
		name, slug := nukeWorkspaceIdentity(covStubClient(s))
		if name != "the active workspace" || slug != "" {
			t.Errorf("got (%q,%q), want fail-closed fallback", name, slug)
		}
	})

	t.Run("empty list fails closed", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{}))
		name, slug := nukeWorkspaceIdentity(covStubClient(s))
		if name != "the active workspace" || slug != "" {
			t.Errorf("got (%q,%q), want fail-closed fallback", name, slug)
		}
	})

	t.Run("undecodable body fails closed", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		// Unregistered route → 404 text body → ReadJSON error.
		name, slug := nukeWorkspaceIdentity(covStubClient(s))
		if name != "the active workspace" || slug != "" {
			t.Errorf("got (%q,%q), want fail-closed fallback", name, slug)
		}
	})
}

// ─── nukeCount ───────────────────────────────────────────────────────────

func TestNukeCount(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "c1"}, {"id": "c2"}, {"id": "c3"},
	}))
	client := covStubClient(s)

	if got := nukeCount(client, "/api/v1/crews"); got != 3 {
		t.Errorf("nukeCount(crews) = %d, want 3", got)
	}
	// Unregistered path → 404 text → decode error → best-effort 0.
	if got := nukeCount(client, "/api/v1/agents"); got != 0 {
		t.Errorf("nukeCount(missing) = %d, want 0", got)
	}
}

// ─── nukeList / nukeListBySlug / nukeCrewIntegrations ────────────────────

func TestNukeList(t *testing.T) {
	t.Run("deletes every listed item", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/labels", clitest.JSONResponse(200, []map[string]string{
			{"id": "l1"}, {"id": "l2"},
		}))
		s.OnDelete("/api/v1/labels/l1", clitest.EmptyResponse(204))
		s.OnDelete("/api/v1/labels/l2", clitest.EmptyResponse(204))

		if err := nukeList(context.Background(), covStubClient(s), "/api/v1/labels", "/api/v1/labels/"); err != nil {
			t.Fatalf("nukeList: %v", err)
		}
		if n := len(s.CallsFor("DELETE", "/api/v1/labels/l1")) + len(s.CallsFor("DELETE", "/api/v1/labels/l2")); n != 2 {
			t.Errorf("expected 2 DELETE calls, got %d", n)
		}
	})

	t.Run("aggregates delete failures", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/labels", clitest.JSONResponse(200, []map[string]string{
			{"id": "l1"}, {"id": "l2"},
		}))
		s.OnDelete("/api/v1/labels/l1", clitest.ErrorResponse(500, "boom"))
		s.OnDelete("/api/v1/labels/l2", clitest.EmptyResponse(204))

		err := nukeList(context.Background(), covStubClient(s), "/api/v1/labels", "/api/v1/labels/")
		if err == nil || !strings.Contains(err.Error(), "1 delete failures") {
			t.Fatalf("expected aggregated failure, got %v", err)
		}
		if !strings.Contains(err.Error(), "/api/v1/labels/l1: HTTP 500") {
			t.Errorf("error should pinpoint the failed delete: %v", err)
		}
	})

	t.Run("undecodable list is an error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		// 200 with a non-array body → passes nukeList's HTTP-status check
		// (CheckError) and then fails to decode into []item. An unregistered
		// path would now surface as the HTTP 404 error instead, never
		// reaching the decode path this case is meant to exercise.
		s.OnGet("/api/v1/labels", clitest.JSONResponse(200, "not-a-list"))
		err := nukeList(context.Background(), covStubClient(s), "/api/v1/labels", "/api/v1/labels/")
		if err == nil || !strings.Contains(err.Error(), "decode /api/v1/labels") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})

	t.Run("cancelled context short-circuits", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := nukeList(ctx, covStubClient(s), "/api/v1/labels", "/api/v1/labels/"); err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if len(s.Calls()) != 0 {
			t.Errorf("no HTTP calls expected after cancel, got %d", len(s.Calls()))
		}
	})
}

func TestNukeListBySlug(t *testing.T) {
	t.Run("deletes by slug and flags empty slugs", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/pipelines", clitest.JSONResponse(200, []map[string]string{
			{"slug": "nightly"}, {"slug": ""},
		}))
		s.OnDelete("/api/v1/pipelines/nightly", clitest.EmptyResponse(204))

		err := nukeListBySlug(context.Background(), covStubClient(s), "/api/v1/pipelines", "/api/v1/pipelines/")
		if err == nil || !strings.Contains(err.Error(), "empty-slug") {
			t.Fatalf("expected empty-slug failure to surface, got %v", err)
		}
		if n := len(s.CallsFor("DELETE", "/api/v1/pipelines/nightly")); n != 1 {
			t.Errorf("the addressable row must still be deleted; DELETE calls = %d", n)
		}
	})

	t.Run("all slugs deletable", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/pipelines", clitest.JSONResponse(200, []map[string]string{
			{"slug": "a"}, {"slug": "b"},
		}))
		s.OnDelete("/api/v1/pipelines/a", clitest.EmptyResponse(204))
		s.OnDelete("/api/v1/pipelines/b", clitest.EmptyResponse(204))
		if err := nukeListBySlug(context.Background(), covStubClient(s), "/api/v1/pipelines", "/api/v1/pipelines/"); err != nil {
			t.Fatalf("nukeListBySlug: %v", err)
		}
	})

	t.Run("delete HTTP failure aggregates", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/pipelines", clitest.JSONResponse(200, []map[string]string{{"slug": "a"}}))
		s.OnDelete("/api/v1/pipelines/a", clitest.ErrorResponse(500, "nope"))
		err := nukeListBySlug(context.Background(), covStubClient(s), "/api/v1/pipelines", "/api/v1/pipelines/")
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("expected HTTP 500 failure, got %v", err)
		}
	})
}

func TestNukeCrewIntegrations(t *testing.T) {
	t.Run("deletes per-crew integrations", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/integrations/crews", clitest.JSONResponse(200, []map[string]string{
			{"id": "i1", "crew_id": "c1"},
			{"id": "i2", "crew_id": "c2"},
		}))
		s.OnDelete("/api/v1/crews/c1/integrations/i1", clitest.EmptyResponse(204))
		s.OnDelete("/api/v1/crews/c2/integrations/i2", clitest.EmptyResponse(204))

		if err := nukeCrewIntegrations(context.Background(), covStubClient(s)); err != nil {
			t.Fatalf("nukeCrewIntegrations: %v", err)
		}
		if n := len(s.CallsFor("DELETE", "/api/v1/crews/c1/integrations/i1")); n != 1 {
			t.Errorf("missing DELETE for crew c1 integration i1")
		}
	})

	t.Run("failure aggregation", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		s.OnGet("/api/v1/integrations/crews", clitest.JSONResponse(200, []map[string]string{
			{"id": "i1", "crew_id": "c1"},
		}))
		s.OnDelete("/api/v1/crews/c1/integrations/i1", clitest.ErrorResponse(500, "x"))
		err := nukeCrewIntegrations(context.Background(), covStubClient(s))
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("expected failure, got %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		// 200 with a non-array body → passes the HTTP-status check, then
		// fails to decode (an unregistered path now surfaces as HTTP 404).
		s.OnGet("/api/v1/integrations/crews", clitest.JSONResponse(200, "not-a-list"))
		err := nukeCrewIntegrations(context.Background(), covStubClient(s))
		if err == nil || !strings.Contains(err.Error(), "decode integrations") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})
}

// ─── seedNuke ────────────────────────────────────────────────────────────

// covRegisterEmptyNukeStubs registers every list endpoint seedNuke walks
// with an empty result so individual tests only override what they need.
func covRegisterEmptyNukeStubs(s *clitest.StubServer) {
	empty := clitest.JSONResponse(200, []map[string]string{})
	s.OnGet("/api/v1/issues", empty)
	s.OnGet("/api/v1/projects", empty)
	s.OnGet("/api/v1/labels", empty)
	s.OnGet("/api/v1/agents", empty)
	s.OnGet("/api/v1/credentials", empty)
	s.OnGet("/api/v1/integrations/crews", empty)
	s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-webhooks", empty)
	s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipeline-schedules", empty)
	s.OnGet("/api/v1/workspaces/"+covWSCli7+"/pipelines", empty)
	s.OnGet("/api/v1/crews", empty)
	// Full-teardown additions: inbox purge, crew-runtime docker teardown.
	// (Escalations are purged per-crew; an empty /crews list means no
	// per-crew DELETE fires, so no stub is needed here.)
	s.OnDelete("/api/v1/inbox", clitest.JSONResponse(200, map[string]int{"deleted": 0}))
	s.OnPost("/api/v1/admin/prune-crew-runtimes", clitest.JSONResponse(200, map[string]any{"removed": []string{}, "count": 0}))
}

func TestSeedNuke_EmptyWorkspaceSucceeds(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covRegisterEmptyNukeStubs(s)

	if err := seedNuke(context.Background(), covStubClient(s)); err != nil {
		t.Fatalf("seedNuke on empty workspace: %v", err)
	}
	// All ten list endpoints must have been consulted, plus the three
	// full-teardown calls: inbox purge (DELETE /inbox), the escalation pass
	// (a second GET /crews), and the crew-runtime teardown (POST prune) — 13.
	if got := len(s.Calls()); got != 13 {
		t.Errorf("expected 13 calls (10 lists + inbox purge + escalation crew list + runtime prune), got %d", got)
	}
}

func TestSeedNuke_TransitionsAndDeletesIssues(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covRegisterEmptyNukeStubs(s)

	ident := "ENG-7"
	s.OnGet("/api/v1/issues", clitest.JSONResponse(200, []map[string]any{
		{"identifier": ident, "crew_id": "c1", "status": "IN_PROGRESS"},
		{"identifier": nil, "crew_id": "c1", "status": "BACKLOG"}, // skipped: nil identifier
		{"identifier": "ENG-8", "crew_id": "c1", "status": "BACKLOG"},
	}))
	issuePath := "/api/v1/crews/c1/issues/" + ident
	s.OnPatch(issuePath, clitest.JSONResponse(200, map[string]string{"status": "ok"}))
	s.OnDelete(issuePath, clitest.EmptyResponse(204))
	s.OnPatch("/api/v1/crews/c1/issues/ENG-8", clitest.JSONResponse(200, map[string]string{}))
	s.OnDelete("/api/v1/crews/c1/issues/ENG-8", clitest.EmptyResponse(204))

	if err := seedNuke(context.Background(), covStubClient(s)); err != nil {
		t.Fatalf("seedNuke: %v", err)
	}

	// IN_PROGRESS → CANCELLED is a single hop in the status DAG, so exactly
	// one PATCH with status=CANCELLED must precede the DELETE.
	patches := s.CallsFor("PATCH", issuePath)
	if len(patches) != 1 {
		t.Fatalf("expected 1 PATCH transition, got %d", len(patches))
	}
	if !strings.Contains(string(patches[0].Body), `"status":"CANCELLED"`) {
		t.Errorf("transition body = %s, want status CANCELLED", patches[0].Body)
	}
	if n := len(s.CallsFor("DELETE", issuePath)); n != 1 {
		t.Errorf("expected 1 DELETE for %s, got %d", ident, n)
	}
	// BACKLOG issue: deletable without any transition.
	if n := len(s.CallsFor("PATCH", "/api/v1/crews/c1/issues/ENG-8")); n != 0 {
		t.Errorf("BACKLOG issue should not be transitioned, got %d PATCHes", n)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/crews/c1/issues/ENG-8")); n != 1 {
		t.Errorf("BACKLOG issue should be deleted directly, got %d", n)
	}
}

func TestSeedNuke_IssueListDecodeFailureAggregates(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covRegisterEmptyNukeStubs(s)
	// Issues list serves non-JSON → decode failure recorded, the rest of
	// the nuke continues and the aggregated error surfaces at the end.
	s.OnGet("/api/v1/issues", clitest.TextResponse(200, "not json"))

	err := seedNuke(context.Background(), covStubClient(s))
	if err == nil || !strings.Contains(err.Error(), "workspace cleanup had 1 failures") {
		t.Fatalf("expected 1 aggregated failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "decode issues") {
		t.Errorf("failure should mention decode issues: %v", err)
	}
}

func TestSeedNuke_CancelledContext(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := seedNuke(ctx, covStubClient(s)); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(s.Calls()) != 0 {
		t.Errorf("no HTTP traffic expected after cancel")
	}
}

func TestNukeHelpers_TransportErrors(t *testing.T) {
	client := covDeadClient(t)
	ctx := context.Background()

	if name, slug := nukeWorkspaceIdentity(client); name != "the active workspace" || slug != "" {
		t.Errorf("identity on dead server = (%q,%q)", name, slug)
	}
	if got := nukeCount(client, "/api/v1/crews"); got != 0 {
		t.Errorf("nukeCount on dead server = %d", got)
	}
	if err := nukeList(ctx, client, "/api/v1/labels", "/api/v1/labels/"); err == nil || !strings.Contains(err.Error(), "GET /api/v1/labels") {
		t.Errorf("nukeList: %v", err)
	}
	if err := nukeListBySlug(ctx, client, "/api/v1/p", "/api/v1/p/"); err == nil || !strings.Contains(err.Error(), "GET /api/v1/p") {
		t.Errorf("nukeListBySlug: %v", err)
	}
	if err := nukeCrewIntegrations(ctx, client); err == nil || !strings.Contains(err.Error(), "GET /api/v1/integrations/crews") {
		t.Errorf("nukeCrewIntegrations: %v", err)
	}
}

func TestNukeHelpers_CancelledContextEntry(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := covStubClient(s)

	if err := nukeListBySlug(ctx, client, "/api/v1/p", "/api/v1/p/"); err != context.Canceled {
		t.Errorf("nukeListBySlug entry: %v", err)
	}
	if err := nukeCrewIntegrations(ctx, client); err != context.Canceled {
		t.Errorf("nukeCrewIntegrations entry: %v", err)
	}
	if len(s.Calls()) != 0 {
		t.Errorf("no HTTP calls expected, got %d", len(s.Calls()))
	}
}

func TestNukeList_CancelMidLoop(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.OnGet("/api/v1/labels", clitest.JSONResponse(200, []map[string]string{
		{"id": "l1"}, {"id": "l2"},
	}))
	// Deleting the first item cancels the context; the loop must stop
	// before touching the second.
	s.OnDelete("/api/v1/labels/l1", func(_ *http.Request, _ []byte) (int, []byte, string) {
		cancel()
		return 204, nil, ""
	})

	if err := nukeList(ctx, covStubClient(s), "/api/v1/labels", "/api/v1/labels/"); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/labels/l2")); n != 0 {
		t.Errorf("second delete must not run after cancel")
	}
}

func TestNukeCrewIntegrations_CancelMidLoop(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.OnGet("/api/v1/integrations/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "i1", "crew_id": "c1"}, {"id": "i2", "crew_id": "c2"},
	}))
	s.OnDelete("/api/v1/crews/c1/integrations/i1", func(_ *http.Request, _ []byte) (int, []byte, string) {
		cancel()
		return 204, nil, ""
	})

	if err := nukeCrewIntegrations(ctx, covStubClient(s)); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/crews/c2/integrations/i2")); n != 0 {
		t.Errorf("second delete must not run after cancel")
	}
}

func TestSeedNuke_EveryPhaseFailingIsAggregated(t *testing.T) {
	// Every list endpoint serves undecodable bytes → all ten phases record
	// a failure and the final error counts them all.
	s := clitest.NewStubServer()
	defer s.Close()
	garbage := clitest.TextResponse(200, "not json")
	for _, p := range []string{
		"/api/v1/issues", "/api/v1/projects", "/api/v1/labels", "/api/v1/agents",
		"/api/v1/credentials", "/api/v1/integrations/crews",
		"/api/v1/workspaces/" + covWSCli7 + "/pipeline-webhooks",
		"/api/v1/workspaces/" + covWSCli7 + "/pipeline-schedules",
		"/api/v1/workspaces/" + covWSCli7 + "/pipelines",
		"/api/v1/crews",
	} {
		s.OnGet(p, garbage)
	}

	err := seedNuke(context.Background(), covStubClient(s))
	// 10 original list phases + 3 full-teardown phases (inbox purge, escalation
	// pass, crew-runtime teardown), all failing on the unrouted/garbage stubs.
	if err == nil || !strings.Contains(err.Error(), "workspace cleanup had 13 failures") {
		t.Fatalf("want 13 aggregated failures, got %v", err)
	}
	for _, frag := range []string{"projects:", "labels:", "agents:", "credentials:", "integrations:", "pipeline-webhooks:", "pipeline-schedules:", "pipelines:", "crews:", "inbox:", "escalations:", "crew runtimes:"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("missing %q in %v", frag, err)
		}
	}
}

func TestSeedNuke_TransitionEdgeCases(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covRegisterEmptyNukeStubs(s)
	s.OnGet("/api/v1/issues", clitest.JSONResponse(200, []map[string]any{
		// DUPLICATE has no outgoing transitions → no path to CANCELLED.
		{"identifier": "DUP-1", "crew_id": "c1", "status": "DUPLICATE"},
		// Transition PATCH rejected by the server → recorded, delete still attempted.
		{"identifier": "REJ-1", "crew_id": "c1", "status": "IN_PROGRESS"},
	}))
	s.OnPatch("/api/v1/crews/c1/issues/REJ-1", clitest.ErrorResponse(422, "transition rejected"))
	s.OnDelete("/api/v1/crews/c1/issues/REJ-1", clitest.ErrorResponse(409, "not deletable"))

	err := seedNuke(context.Background(), covStubClient(s))
	if err == nil {
		t.Fatal("expected aggregated failures")
	}
	if !strings.Contains(err.Error(), "no transition path DUPLICATE→CANCELLED for DUP-1") {
		t.Errorf("missing no-path failure: %v", err)
	}
	if !strings.Contains(err.Error(), "transition REJ-1→CANCELLED: HTTP 422") {
		t.Errorf("missing rejected-transition failure: %v", err)
	}
	if !strings.Contains(err.Error(), "delete issue REJ-1: HTTP 409") {
		t.Errorf("missing delete failure: %v", err)
	}
	// DUP-1 must never be PATCHed or DELETEd by the slow path — no route is
	// registered for it, so any attempt would show up as an extra failure.
	if strings.Contains(err.Error(), "DUP-1: HTTP") {
		t.Errorf("DUP-1 should not be transitioned/deleted: %v", err)
	}
}

func TestSeedNuke_CancelDuringIssuePage(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	covRegisterEmptyNukeStubs(s)
	// Serving the issue page cancels the context — the per-issue loop must
	// bail before processing the first row.
	s.OnGet("/api/v1/issues", func(r *http.Request, _ []byte) (int, []byte, string) {
		cancel()
		return clitest.JSONResponse(200, []map[string]any{
			{"identifier": "ENG-1", "crew_id": "c1", "status": "BACKLOG"},
		})(r, nil)
	})

	if err := seedNuke(ctx, covStubClient(s)); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	// The per-issue loop must bail before touching any row: no issue-level
	// DELETE may fire. (The crew-scoped teardown pieces — inbox/escalations/
	// runtimes — run BEFORE issue paging in nukeAll, so they legitimately
	// completed under a still-valid context; this test guards the issue loop's
	// cancellation, not those.)
	for _, c := range s.Calls() {
		if c.Method == "DELETE" && strings.Contains(c.Path, "/issues/") {
			t.Errorf("no issue deletes expected after cancel: %s", c.Path)
		}
	}
}

func TestSeedNuke_UndeletableFullPageAdvancesOffset(t *testing.T) {
	// A full page (>= pageLimit) where nothing can be deleted must advance
	// the offset instead of refetching the same rows forever. We serve one
	// full page of undeletable issues (delete → 500), then an empty page.
	s := clitest.NewStubServer()
	defer s.Close()
	covRegisterEmptyNukeStubs(s)

	page := make([]map[string]any, 500)
	for i := range page {
		page[i] = map[string]any{"identifier": "X-1", "crew_id": "c1", "status": "BACKLOG"}
	}
	first := true
	s.OnGet("/api/v1/issues", func(r *http.Request, _ []byte) (int, []byte, string) {
		if first {
			first = false
			return clitest.JSONResponse(200, page)(r, nil)
		}
		return clitest.JSONResponse(200, []map[string]any{})(r, nil)
	})
	s.OnDelete("/api/v1/crews/c1/issues/X-1", clitest.ErrorResponse(500, "undeletable"))

	err := seedNuke(context.Background(), covStubClient(s))
	if err == nil || !strings.Contains(err.Error(), "failures") {
		t.Fatalf("expected aggregated delete failures, got %v", err)
	}
	// Two GETs: offset=0 (full page, zero deletions) then offset=500 (empty).
	gets := s.CallsFor("GET", "/api/v1/issues")
	if len(gets) != 2 {
		t.Fatalf("expected 2 issue list pages, got %d", len(gets))
	}
	if !strings.Contains(gets[1].Query, "offset=500") {
		t.Errorf("second page should advance offset: query=%q", gets[1].Query)
	}
}
