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

	if outcome, err := nukeRuntimes(context.Background(), covStubClient(s)); err != nil || outcome != runtimeTeardownDone {
		t.Fatalf("nukeRuntimes: %v %v", outcome, err)
	}
	if n := len(s.CallsFor("POST", "/api/v1/admin/prune-crew-runtimes")); n != 1 {
		t.Fatalf("POST prune calls = %d; want 1", n)
	}
}

// A20: nuke reports success only when it can vouch for the teardown. No
// runtime provider (docker may have failed at boot, its runtimes still on the
// daemon) and a partial teardown are errors; only a provider that never runs
// docker runtimes is passed over, explicitly.
func TestNukeRuntimes_OnlyUnsupportedIsPassedOver(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    map[string]any
		wantErr bool
		want    runtimeTeardown
	}{
		"unavailable":        {503, map[string]any{"error": "no provider", "reason": "unavailable"}, true, ""},
		"503 without reason": {503, map[string]any{"error": "docker not configured"}, true, ""},
		"unsupported":        {503, map[string]any{"error": "apple", "reason": "unsupported"}, false, runtimeTeardownUnsupported},
		"partial teardown":   {500, map[string]any{"error": "crew runtime prune failed", "removed": []string{"a"}, "count": 1}, true, ""},
		"complete teardown":  {200, map[string]any{"removed": []string{"a", "b"}, "count": 2}, false, runtimeTeardownDone},
	} {
		t.Run(name, func(t *testing.T) {
			s := clitest.NewStubServer()
			defer s.Close()
			s.OnPost("/api/v1/admin/prune-crew-runtimes", clitest.JSONResponse(tc.status, tc.body))
			outcome, err := nukeRuntimes(context.Background(), covStubClient(s))
			if (err != nil) != tc.wantErr || outcome != tc.want {
				t.Fatalf("outcome=%q err=%v", outcome, err)
			}
		})
	}
}

// A20: when the runtime teardown fails, nuke stops before anything else:
// no escalation, inbox or crew is deleted, so the crews that locate the
// runtimes stay for a retry, and the command exits non-zero.
func TestNukeAll_StopsBeforeDeletingWhenRuntimesFail(t *testing.T) {
	for name, resp := range map[string]struct {
		status int
		body   map[string]any
	}{
		"unavailable": {503, map[string]any{"reason": "unavailable"}},
		"partial":     {500, map[string]any{"removed": []string{"x"}, "count": 1}},
	} {
		t.Run(name, func(t *testing.T) {
			s := clitest.NewStubServer()
			defer s.Close()
			covRegisterEmptyNukeStubs(s)
			s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{{"id": "c1"}}))
			s.OnDelete("/api/v1/crews/c1", clitest.JSONResponse(200, map[string]bool{"success": true}))
			s.OnDelete("/api/v1/crews/c1/escalations", clitest.JSONResponse(200, map[string]int{"deleted": 0}))
			s.OnPost("/api/v1/admin/prune-crew-runtimes", clitest.JSONResponse(resp.status, resp.body))

			err := nukeAll(context.Background(), covStubClient(s))
			if err == nil || !strings.Contains(err.Error(), "crews are kept") {
				t.Fatalf("nukeAll err = %v, want a failure that keeps the crews", err)
			}
			for _, call := range [][2]string{{"DELETE", "/api/v1/crews/c1"}, {"DELETE", "/api/v1/crews/c1/escalations"}, {"DELETE", "/api/v1/inbox"}} {
				if n := len(s.CallsFor(call[0], call[1])); n != 0 {
					t.Errorf("%s %s called %d times after a failed runtime teardown", call[0], call[1], n)
				}
			}
		})
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

// The keeper decision history is part of "all workspace contents", and it was
// not being deleted: 115 rows survived a nuke on dev2, each holding an
// agent-authored intent and the conversation history the judge was shown.
//
// keeper_requests has no workspace_id and its agent FK has no ON DELETE CASCADE,
// so it is reachable only while the agents still exist — which is why the call
// belongs before nukeData, alongside escalations and crew runtimes.
func TestNukeKeeperRequests_HitsTheDeleteRoute(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/admin/keeper/requests",
		clitest.JSONResponse(200, map[string]int{"deleted_requests": 3, "deleted_events": 5}))

	if err := nukeKeeperRequests(context.Background(), covStubClient(s)); err != nil {
		t.Fatalf("nukeKeeperRequests: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/admin/keeper/requests")); n != 1 {
		t.Fatalf("DELETE /api/v1/admin/keeper/requests calls = %d; want 1", n)
	}
}

// A server older than the endpoint answers 404. Failing the whole nuke on that
// would make the CLI unusable against any instance it has not been upgraded
// alongside — the same tolerance nukeRuntimes gives a docker-less 503.
func TestNukeKeeperRequests_ToleratesAnOlderServer(t *testing.T) {
	for _, code := range []int{404, 405} {
		s := clitest.NewStubServer()
		s.OnDelete("/api/v1/admin/keeper/requests", clitest.JSONResponse(code, map[string]string{"error": "no such route"}))
		if err := nukeKeeperRequests(context.Background(), covStubClient(s)); err != nil {
			t.Errorf("HTTP %d should be tolerated, got: %v", code, err)
		}
		s.Close()
	}
}

// Anything else is a real failure and must be reported, not swallowed — a nuke
// that silently leaves the decision history behind is the bug this fixes.
func TestNukeKeeperRequests_ReportsARealFailure(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/admin/keeper/requests", clitest.JSONResponse(403, map[string]string{"error": "Forbidden"}))

	if err := nukeKeeperRequests(context.Background(), covStubClient(s)); err == nil {
		t.Error("a 403 was swallowed; the operator would believe the history was cleared")
	}
}
