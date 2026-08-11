package api

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// The tenant fence the master token does not provide
//
// PR-F24's foreign-ID closure — assertBoundCrewWorkspaceDB and
// assertBoundChatWorkspaceDB — proves that a body-carried crew_id and chat_id
// belong to the workspace the caller's token is bound to. Both are documented
// no-ops for MASTER-token callers, on the stated grounds that those are
// "host-side trusted services" (middleware.go).
//
// The `crewship` routine step is the first master-token caller whose request
// body is written by a USER. crewshipInjected pins the fields the dispatcher
// owns, and correctly refuses to make any of them conditional — but chat_id is
// not among them, and it cannot be: a run has no chat. It is a REQUIRED,
// author-supplied arg on the two verbs that carry one (crewship_step.go), and
// on the far side of the loopback it is a tenant-scoping input:
//
//   - escalation_handler.go broadcasts escalation_created into the session
//     channel named by chat_id, and files the row's chat_id from the body;
//   - assignments_run.go resolves assigned_by_id and the assigner's crew from
//     `SELECT agent_id FROM chats WHERE id = ?` — no workspace predicate — and
//     later writes a mission_comments row keyed on the same value.
//
// So the fence for an author-supplied cross-tenant reference has to be at this
// door. Where a value can be injected it is injected; where it cannot, it is
// verified.
// ---------------------------------------------------------------------------

// tenancyDB builds the tables the dispatcher's fence reads: crews (with the
// workspace column policy.Resolver's fixture omits) and chats. Two workspaces,
// so "belongs to another tenant" is a real row rather than a missing one —
// a fence that only rejects unknown ids is not a fence.
func tenancyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    workspace_id TEXT,
    autonomy_level TEXT,
    behavior_mode TEXT,
    autonomy_set_by_user_id TEXT,
    autonomy_set_at TEXT,
    autonomy_reason TEXT
);
CREATE TABLE chats (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
CREATE TABLE escalations (id TEXT PRIMARY KEY, crew_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'PENDING');
INSERT INTO crews (id, workspace_id, autonomy_level, behavior_mode) VALUES
    ('crew_a', 'ws_a', 'full', 'warn'),
    ('crew_b', 'ws_b', 'full', 'warn');
INSERT INTO chats (id, workspace_id) VALUES ('chat_a', 'ws_a'), ('chat_b', 'ws_b');
`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// A routine in workspace A naming a chat in workspace B must be refused, and
// refused BEFORE the loopback call — the damage on the far side (a broadcast
// into another tenant's live session, a row attributed across the boundary) is
// done by the request itself, so a refusal that still sends it is not one.
func TestCrewshipActions_ForeignChatIsRefused(t *testing.T) {
	for _, tc := range []struct {
		verb string
		args map[string]any
	}{
		{"escalation.create", map[string]any{"from_slug": "lead", "reason": "stuck", "chat_id": "chat_b"}},
		{"assignment.create", map[string]any{"target_slug": "dev", "task": "do it", "chat_id": "chat_b"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			var calls []capturedCall
			srv := fakeInternalAPI(t, &calls)
			db := tenancyDB(t)
			actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())

			_, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
				Verb:        tc.verb,
				Args:        tc.args,
				WorkspaceID: "ws_a",
				CrewID:      "crew_a",
				RunID:       "run_1",
			})
			if err == nil {
				t.Fatalf("%s with a foreign chat_id was dispatched, not refused", tc.verb)
			}
			if !strings.Contains(err.Error(), "chat_id") {
				t.Fatalf("refusal does not name the offending field: %v", err)
			}
			if len(calls) != 0 {
				t.Fatalf("the refused call still reached the internal API: %+v", calls)
			}
		})
	}
}

// The positive control. A chat in the run's OWN workspace still dispatches, so
// the fence is a workspace check and not a blanket refusal of chat_id.
func TestCrewshipActions_OwnWorkspaceChatStillDispatches(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := tenancyDB(t)
	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())

	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb:        "escalation.create",
		Args:        map[string]any{"from_slug": "lead", "reason": "stuck", "chat_id": "chat_a"},
		WorkspaceID: "ws_a",
		CrewID:      "crew_a",
		RunID:       "run_1",
	}); err != nil {
		t.Fatalf("an in-workspace chat was refused: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
}

// The crew the run acts as must resolve inside the run's workspace.
//
// Two things ride on this. The row a verb creates is attributed to crew_id, so
// a crew from another tenant files the write across the boundary. And the
// autonomy gate is resolved on crew_id: policy.Resolver answers ErrNoRows with
// the guided default rather than an error, and guided PERMITS every action
// these six verbs use — so an unresolvable crew is not held, it is waved
// through. Proving the crew here is what keeps "bounded by the crew's autonomy
// level" true of a crew that exists.
func TestCrewshipActions_CrewMustResolveInTheRunsWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		crew string
	}{
		{"foreign crew", "crew_b"},
		{"nonexistent crew", "crew_ghost"},
		{"no crew at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []capturedCall
			srv := fakeInternalAPI(t, &calls)
			db := tenancyDB(t)
			actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), db, slog.Default())

			_, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
				Verb:        "issue.comment",
				Args:        map[string]any{"identifier": "ENG-1", "body": "hi"},
				WorkspaceID: "ws_a",
				CrewID:      tc.crew,
				RunID:       "run_1",
			})
			if err == nil {
				t.Fatalf("crew %q was accepted as the acting principal in ws_a", tc.crew)
			}
			if len(calls) != 0 {
				t.Fatalf("the refused call still reached the internal API: %+v", calls)
			}
		})
	}
}
