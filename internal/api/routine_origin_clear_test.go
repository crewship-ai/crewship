package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestOperatorClearInvalidatesDraftAndRefusesImmutablePinnedSchedule(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewPipelineHandler(f.h.db, newTestLogger(), nil, nil)
	now := time.Now()
	input := pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "clear-draft", Name: "Draft", DefinitionJSON: `{"name":"Draft","steps":[]}`, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, SourceRunID: f.run, SourceAgentID: f.agent, LastTestRunAt: &now, LastTestRunPassed: true}
	recipe, err := h.store.Save(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	var origin string
	if err = f.h.db.QueryRow(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	draft, err := h.store.GetDraft(t.Context(), f.workspace, recipe.Slug)
	if err != nil {
		t.Fatal(err)
	}
	draft.UpdatedBy = f.user
	draft, err = h.store.SaveDraft(t.Context(), *draft)
	if err != nil {
		t.Fatal(err)
	}
	call := func(body any) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", jsonBody(body))
		req.SetPathValue("slug", recipe.Slug)
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: f.user}), f.workspace, "OWNER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindSession)
		rr := httptest.NewRecorder()
		h.ClearOriginBinding(rr, req.WithContext(ctx))
		return rr
	}
	body := map[string]any{"action": "clear", "reason": "Reviewed current draft and head", "expected_origin_id": origin}
	if rr := call(body); rr.Code != 409 {
		t.Fatalf("silent stale draft accepted %d %s", rr.Code, rr.Body.String())
	}
	body["draft"] = map[string]any{"id": draft.ID, "expected_revision": draft.Revision, "expected_origin_id": "foreign-origin"}
	if rr := call(body); rr.Code != 409 {
		t.Fatalf("wrong draft origin accepted %d %s", rr.Code, rr.Body.String())
	}
	body["draft"] = map[string]any{"id": draft.ID, "expected_revision": draft.Revision, "expected_origin_id": origin}
	version := 1
	pinned, err := pipeline.NewScheduleStore(f.h.db).Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: f.workspace, Name: "Pinned", TargetPipelineID: recipe.ID, TargetPipelineVersion: &version, CronExpr: "0 1 * * *", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	body["schedules"] = []any{map[string]any{"id": pinned.ID, "expected_origin_id": origin}}
	if rr := call(body); rr.Code != 409 {
		t.Fatalf("immutable pinned authority claimed cleared: %d %s", rr.Code, rr.Body.String())
	}
	delete(body, "schedules")
	if rr := call(body); rr.Code != 200 {
		t.Fatalf("reviewed draft clear %d %s", rr.Code, rr.Body.String())
	}
	fresh, err := h.store.GetDraft(t.Context(), f.workspace, recipe.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ChatOriginID != "" || fresh.Revision != draft.Revision+1 {
		t.Fatalf("draft still bound/stale: %+v", fresh)
	}
	input.SourceRunID = ""
	input.SourceAgentID = ""
	input.Publication = &pipeline.DraftPublication{ID: draft.ID, Revision: draft.Revision, Document: string(draft.Document)}
	if _, err = h.store.Save(t.Context(), input); !errors.Is(err, pipeline.ErrDraftConflict) {
		t.Fatalf("stale publication resurrected authority: %v", err)
	}
	input.Publication = &pipeline.DraftPublication{ID: fresh.ID, Revision: fresh.Revision, Document: string(fresh.Document)}
	if _, err = h.store.Save(t.Context(), input); err != nil {
		t.Fatalf("fresh cleared publication: %v", err)
	}
	var head sql.NullString
	if err = f.h.db.QueryRow(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID).Scan(&head); err != nil || head.Valid {
		t.Fatalf("publication restored stale origin: %+v %v", head, err)
	}
	var pinnedOrigin string
	if err = f.h.db.QueryRow(`SELECT chat_origin_id FROM pipeline_schedules WHERE id=?`, pinned.ID).Scan(&pinnedOrigin); err != nil || pinnedOrigin != origin {
		t.Fatalf("unselected pinned authority changed: %s %v", pinnedOrigin, err)
	}
}
