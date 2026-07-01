package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// The `nuke` subcommands dispatch to these piece functions; the tests assert
// each piece hits exactly the endpoint(s) it should — the CLI-surface contract
// the coordinator asked to pin (nuke inbox → DELETE /inbox, nuke runtimes →
// POST prune, nuke all → all in dependency order, nuke data → none of them).

func TestNukeInbox_HitsDeleteInbox(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/inbox", clitest.JSONResponse(200, map[string]int{"deleted": 3}))

	if err := nukeInbox(context.Background(), covStubClient(s), ""); err != nil {
		t.Fatalf("nukeInbox: %v", err)
	}
	calls := s.CallsFor("DELETE", "/api/v1/inbox")
	if len(calls) != 1 {
		t.Fatalf("DELETE /api/v1/inbox calls = %d; want 1", len(calls))
	}
	// The client always appends workspace_id; an unscoped purge must not carry
	// a kind filter.
	if strings.Contains(calls[0].Query, "kind=") {
		t.Errorf("unscoped purge query = %q; must not carry a kind filter", calls[0].Query)
	}
}

func TestNukeInbox_KindScopesQuery(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/inbox", clitest.JSONResponse(200, map[string]int{"deleted": 1}))

	if err := nukeInbox(context.Background(), covStubClient(s), "failed_run"); err != nil {
		t.Fatalf("nukeInbox: %v", err)
	}
	calls := s.CallsFor("DELETE", "/api/v1/inbox")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "kind=failed_run") {
		t.Fatalf("want one DELETE with kind=failed_run in query, got %+v", calls)
	}
}

func TestNukeRuntimes_HitsPrunePost(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/admin/prune-crew-runtimes", clitest.JSONResponse(200, map[string]any{"removed": []string{}, "count": 0}))

	if err := nukeRuntimes(context.Background(), covStubClient(s)); err != nil {
		t.Fatalf("nukeRuntimes: %v", err)
	}
	if n := len(s.CallsFor("POST", "/api/v1/admin/prune-crew-runtimes")); n != 1 {
		t.Fatalf("POST prune calls = %d; want 1", n)
	}
}

// A docker-less server answers 503; nukeRuntimes must treat it as a no-op, not
// an error (nuke has to work on a box without docker).
func TestNukeRuntimes_503IsTolerated(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/admin/prune-crew-runtimes", clitest.ErrorResponse(503, "docker not configured"))

	if err := nukeRuntimes(context.Background(), covStubClient(s)); err != nil {
		t.Errorf("503 must be tolerated, got %v", err)
	}
}

func TestNukeEscalations_AllCrews(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{{"id": "c1"}, {"id": "c2"}}))
	s.OnDelete("/api/v1/crews/c1/escalations", clitest.JSONResponse(200, map[string]int{"deleted": 2}))
	s.OnDelete("/api/v1/crews/c2/escalations", clitest.JSONResponse(200, map[string]int{"deleted": 0}))

	if err := nukeEscalations(context.Background(), covStubClient(s), ""); err != nil {
		t.Fatalf("nukeEscalations: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/crews/c1/escalations")); n != 1 {
		t.Errorf("c1 escalation deletes = %d; want 1", n)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/crews/c2/escalations")); n != 1 {
		t.Errorf("c2 escalation deletes = %d; want 1", n)
	}
}

// nuke data must touch ONLY DB entities — never inbox, escalations, or docker
// runtimes.
func TestNukeData_TouchesNoTeardownEndpoints(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covRegisterEmptyNukeStubs(s)

	failures, err := nukeData(context.Background(), covStubClient(s))
	if err != nil {
		t.Fatalf("nukeData: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("unexpected failures on empty workspace: %v", failures)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/inbox")); n != 0 {
		t.Errorf("nukeData hit inbox purge %d times; want 0", n)
	}
	if n := len(s.CallsFor("POST", "/api/v1/admin/prune-crew-runtimes")); n != 0 {
		t.Errorf("nukeData hit runtime prune %d times; want 0", n)
	}
	for _, c := range s.Calls() {
		if c.Method == "DELETE" && len(c.Path) > 15 && c.Path[len(c.Path)-12:] == "/escalations" {
			t.Errorf("nukeData hit an escalation purge (%s); want none", c.Path)
		}
	}
}

// nuke all must hit every teardown endpoint, and the crew-scoped ones
// (escalations, runtimes) MUST fire before crews are deleted.
func TestNukeAll_HitsAllTeardownsBeforeCrewDelete(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covRegisterEmptyNukeStubs(s)
	// One crew so an escalation purge actually fires.
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{{"id": "c1", "slug": "eng"}}))
	s.OnDelete("/api/v1/crews/c1/escalations", clitest.JSONResponse(200, map[string]int{"deleted": 0}))
	s.OnDelete("/api/v1/crews/c1", clitest.EmptyResponse(204))

	if err := nukeAll(context.Background(), covStubClient(s)); err != nil {
		t.Fatalf("nukeAll: %v", err)
	}

	// All four teardown surfaces were exercised.
	if n := len(s.CallsFor("DELETE", "/api/v1/inbox")); n != 1 {
		t.Errorf("inbox purge = %d; want 1", n)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/crews/c1/escalations")); n != 1 {
		t.Errorf("escalation purge = %d; want 1", n)
	}
	if n := len(s.CallsFor("POST", "/api/v1/admin/prune-crew-runtimes")); n != 1 {
		t.Errorf("runtime prune = %d; want 1", n)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/crews/c1")); n != 1 {
		t.Errorf("crew delete = %d; want 1", n)
	}

	// Dependency order: the escalation purge and runtime prune must precede the
	// crew row deletion (which would otherwise orphan them).
	idxOf := func(method, path string) int {
		for i, c := range s.Calls() {
			if c.Method == method && c.Path == path {
				return i
			}
		}
		return -1
	}
	crewDel := idxOf("DELETE", "/api/v1/crews/c1")
	escDel := idxOf("DELETE", "/api/v1/crews/c1/escalations")
	runtime := idxOf("POST", "/api/v1/admin/prune-crew-runtimes")
	if escDel < 0 || crewDel < 0 || escDel > crewDel {
		t.Errorf("escalation purge (idx %d) must precede crew delete (idx %d)", escDel, crewDel)
	}
	if runtime < 0 || runtime > crewDel {
		t.Errorf("runtime prune (idx %d) must precede crew delete (idx %d)", runtime, crewDel)
	}
}
