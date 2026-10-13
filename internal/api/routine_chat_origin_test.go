package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestRoutineChatOriginPersistsReauthorizesAndRevokes(t *testing.T) {
	f := newChatIssueFixture(t)
	store := pipeline.NewStore(f.h.db)
	now := time.Now()
	in := pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "origin-routine", Name: "Origin routine", DefinitionJSON: `{"name":"Origin routine","steps":[]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, LastTestRunAt: &now, LastTestRunPassed: true, SourceRunID: f.run, SourceAgentID: f.agent}
	recipe, err := store.Save(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	read := func(query string, args ...any) string {
		t.Helper()
		var id string
		if err := f.h.db.QueryRow(query, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			t.Fatal("lost origin")
		}
		return id
	}
	original := read(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID)
	if v := read(`SELECT chat_origin_id FROM pipeline_versions WHERE pipeline_id=? AND version=1`, recipe.ID); v != original {
		t.Fatal("version differs from initial origin")
	}
	// The source run can end; scheduled work keeps the original human boundary.
	execOrFatal(t, f.h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('origin-terminal',?,?,?,'run.completed','orchestrator','done','{}')`, f.workspace, f.agent, f.run)
	if _, err = runauthority.ValidateOrigin(t.Context(), f.h.db, original, f.agent, f.workspace); err != nil {
		t.Fatal(err)
	}
	// A public edit without a current chat cannot erase the binding.
	in.SourceRunID = ""
	in.SourceAgentID = ""
	if _, err = store.Save(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if got := read(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID); got != original {
		t.Fatal("public edit erased authority")
	}
	// Reauthorization of unchanged content updates head only; version stays immutable.
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, "origin-new-chat", "origin-new-run")
	in.SourceRunID = "origin-new-run"
	in.SourceAgentID = f.agent
	if _, err = store.Save(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	fresh := read(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID)
	if fresh == original {
		t.Fatal("fresh chat did not replace head authority")
	}
	if got := read(`SELECT chat_origin_id FROM pipeline_versions WHERE pipeline_id=? AND version=1`, recipe.ID); got != original {
		t.Fatal("historical version authority changed")
	}
	schedules := pipeline.NewScheduleStore(f.h.db)
	latest, err := schedules.Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: f.workspace, Name: "latest", TargetPipelineID: recipe.ID, CronExpr: "0 7 * * *", Timezone: "UTC", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	version := 1
	pinned, err := schedules.Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: f.workspace, Name: "pinned", TargetPipelineID: recipe.ID, TargetPipelineVersion: &version, CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if latest.ChatOriginID != fresh || pinned.ChatOriginID != original {
		t.Fatal("schedule lost head/pinned origin distinction")
	}
	// Revocation applies after the parent ended and before actual routine execution.
	execOrFatal(t, f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.h.db), nil, nil)
	_, err = executor.Run(context.Background(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
	if !errors.Is(err, runauthority.ErrRequesterStale) {
		t.Fatalf("revoked origin not rejected: %v", err)
	}
}

func TestRoutineChatOriginPendingCoalesceFollowsSelectedPayload(t *testing.T) {
	f := newChatIssueFixture(t)
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := runauthority.CaptureOrigin(t.Context(), tx, f.run, f.agent, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, "pending-second-chat", "pending-second-run")
	tx, err = f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runauthority.CaptureOrigin(t.Context(), tx, "pending-second-run", f.agent, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := pipeline.NewStore(f.h.db)
	now := time.Now()
	recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "pending-origin", Name: "Pending", DefinitionJSON: `{"name":"Pending","steps":[]}`, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	pending := pipeline.NewPendingRunStore(f.h.db)
	in := pipeline.PendingRun{ID: "origin-pending-first", WorkspaceID: f.workspace, PipelineID: recipe.ID, PipelineSlug: recipe.Slug, DebounceKey: "same", FireAt: now.Add(time.Minute), ChatOriginID: first.ID, InputsJSON: `{"from":"first"}`}
	id, coalesced, err := pending.Enqueue(t.Context(), in)
	if err != nil || coalesced {
		t.Fatalf("first enqueue: %v %v", coalesced, err)
	}
	in.ID = "origin-pending-second"
	in.ChatOriginID = second.ID
	in.InputsJSON = `{"from":"second"}`
	got, coalesced, err := pending.Enqueue(t.Context(), in)
	if err != nil || !coalesced || got != id {
		t.Fatalf("coalesce: %s %v %v", got, coalesced, err)
	}
	due, err := pending.DueRuns(t.Context(), now.Add(time.Hour), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due: %v %v", due, err)
	}
	if due[0].ChatOriginID != second.ID || due[0].InputsJSON != in.InputsJSON {
		t.Fatal("payload and authority diverged")
	}
	claimed, err := pending.ClaimDue(t.Context(), id, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ChatOriginID != second.ID {
		t.Fatal("claim lost authority")
	}
}

func TestRoutineChatOriginDraftSurvivesHumanEditAndPublish(t *testing.T) {
	f := newChatIssueFixture(t)
	store := pipeline.NewStore(f.h.db)
	d, err := store.GetDraft(t.Context(), f.workspace, "draft-origin")
	if err != nil {
		t.Fatal(err)
	}
	d.UpdatedBy = "agent:" + f.agent
	d.SourceRunID = f.run
	d.SourceAgentID = f.agent
	d.Document = []byte(`{"slug":"draft-origin","name":"Draft","definition":{"name":"Draft","steps":[]}}`)
	d, err = store.SaveDraft(t.Context(), *d)
	if err != nil {
		t.Fatal(err)
	}
	origin := d.ChatOriginID
	if origin == "" {
		t.Fatal("agent draft lost origin")
	}
	// Public JSON cannot provide server-private fields. Its edits retain the CAS binding.
	d.ChatOriginID = ""
	d.SourceRunID = ""
	d.SourceAgentID = ""
	d.UpdatedBy = f.user
	d, err = store.SaveDraft(t.Context(), *d)
	if err != nil {
		t.Fatal(err)
	}
	if d.ChatOriginID != origin {
		t.Fatal("human edit erased agent origin")
	}
	execOrFatal(t, f.h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('draft-origin-terminal',?,?,?,'run.completed','orchestrator','done','{}')`, f.workspace, f.agent, f.run)
	now := time.Now()
	recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "draft-origin", Name: "Draft", DefinitionJSON: `{"name":"Draft","steps":[]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, Via: pipeline.AuthoredViaUser, UserID: f.user}, LastTestRunAt: &now, LastTestRunPassed: true, Publication: &pipeline.DraftPublication{ID: d.ID, Revision: d.Revision, Document: string(d.Document)}})
	if err != nil {
		t.Fatal(err)
	}
	var actual string
	if err = f.h.db.QueryRow(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID).Scan(&actual); err != nil || actual != origin {
		t.Fatalf("publication lost origin: %s %v", actual, err)
	}
}

// Execute the real DSL dispatcher against real authenticated mutation handlers.
// Revocation at the HTTP boundary must beat an already admitted pipeline run.
func TestRoutineChatOriginDirectDSLChecksAtActualIssueMutation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		human, revoke bool
	}{{"human", true, false}, {"revoked", true, true}, {"autonomous", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			revoke := tc.revoke
			f := newChatIssueFixture(t)
			host := NewInternalHandler(f.h.db, "origin-host-token", newTestLogger())
			mux := http.NewServeMux()
			mux.Handle("POST /api/v1/internal/issues", host.requireInternal(withHostChatOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if revoke {
					execOrFatal(t, f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
				}
				f.h.Create(w, r)
			}))))
			server := httptest.NewServer(mux)
			defer server.Close()
			store := pipeline.NewStore(f.h.db)
			now := time.Now()
			sourceRun := ""
			if tc.human {
				sourceRun = f.run
			}
			recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "dsl-origin", Name: "DSL", DefinitionJSON: `{"name":"DSL","steps":[{"id":"create","type":"crewship","action":"issue.create","args":{"title":"Routine issue"}}]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, SourceRunID: sourceRun, SourceAgentID: f.agent, LastTestRunAt: &now, LastTestRunPassed: true})
			if err != nil {
				t.Fatal(err)
			}
			actions := newCrewshipActions(server.URL, "origin-host-token", policy.NewResolver(f.h.db), f.h.db, newTestLogger())
			executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.h.db), nil, nil).WithRunStore(pipeline.NewRunStore(f.h.db)).WithCrewshipActions(actions)
			res, err := executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
			if err != nil {
				t.Fatal(err)
			}
			var issues, operations, refs int
			f.h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE title='Routine issue'`).Scan(&issues)
			f.h.db.QueryRow(`SELECT COUNT(*) FROM agent_operations`).Scan(&operations)
			f.h.db.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs`).Scan(&refs)
			if revoke {
				if res.Status != "FAILED" || issues != 0 || operations != 0 || refs != 0 {
					t.Fatalf("revoked mutation ran: %+v issues=%d operations=%d refs=%d", res, issues, operations, refs)
				}
			} else {
				expected := 0
				if tc.human {
					expected = 1
				}
				if res.Status != "COMPLETED" || issues != 1 || operations != expected || refs != expected {
					t.Fatalf("DSL authority/receipt missing: %+v issues=%d operations=%d refs=%d", res, issues, operations, refs)
				}
			}
		})
	}
}
