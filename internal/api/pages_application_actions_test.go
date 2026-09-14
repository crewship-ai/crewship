package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"net/http/httptest"
	"testing"
)

func TestPageApplicationActionsPublicationFenceAndOwnReceipt(t *testing.T) {
	h, _, ws, user := newPageActionFixture(t)
	var declaration pages.Document
	var raw string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE workspace_id=? AND slug=?`, ws, pageActionSlug).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &declaration); err != nil {
		t.Fatal(err)
	}
	panel, _ := declaration.FindPanel(pageActionPanel)
	panel.Actions = panel.Actions[:1]
	encoded, _ := json.Marshal(declaration)
	if _, err := h.db.Exec(`UPDATE pages SET spec_json=? WHERE workspace_id=? AND slug=?`, string(encoded), ws, pageActionSlug); err != nil {
		t.Fatal(err)
	}
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	h.SetBuildWorker(&testPageBuilder{}, &pagebuild.Store{Directory: t.TempDir()})
	h.pageRuntimeOrigin = "https://pages.example.net"
	h.pageStudioOrigin = "https://studio.example.com"
	if w := projectPut(t, h, ws, user, "OWNER", pageActionSlug, 0, projectTestSource()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	r := pagesRequest(t, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`)
	r.SetPathValue("slug", pageActionSlug)
	w := httptest.NewRecorder()
	h.BuildProject(w, r)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var job pageBuildRecord
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if !waitForBackgroundWork(5e9) {
		t.Fatal("build timeout")
	}
	definitionDigest, routineDigests := pageFenceForTest(t, h, ws, pageActionSlug)
	body, _ := json.Marshal(map[string]any{"build_id": job.ID, "expected_revision": 1, "expected_publication": 0, "reviewed_code": true, "expected_definition_digest": definitionDigest, "expected_routine_digests": routineDigests})
	r = pagesRequest(t, "POST", "/", ws, user, "OWNER", string(body))
	r.SetPathValue("slug", pageActionSlug)
	w = httptest.NewRecorder()
	h.PublishProject(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	dispatch := func(actor, role, body, key string) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", "/", ws, actor, role, body)
		r.SetPathValue("slug", pageActionSlug)
		r.SetPathValue("panelId", pageActionPanel)
		r.SetPathValue("actionId", pageActionID)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.DispatchApplicationAction(w, r)
		return w
	}
	valid := `{"publication":1,"inputs":{"reason":"test"}}`
	if w := dispatch(user, "OWNER", `{"publication":2,"inputs":{"reason":"test"}}`, "a"); w.Code != 409 {
		t.Fatalf("stale %d %s", w.Code, w.Body.String())
	}
	if w := dispatch(user, "OWNER", `{"publication":1,"shell":"whoami"}`, "a"); w.Code != 400 {
		t.Fatalf("forged %d", w.Code)
	}
	if w := dispatch(user, "OWNER", valid, ""); w.Code != 400 {
		t.Fatalf("missing key %d", w.Code)
	}
	// The published Page does not freeze routine execution; expose and record drift.
	if _, err := h.db.Exec(`UPDATE pipelines SET definition_json=definition_json||' ' WHERE workspace_id=? AND slug=?`, ws, pageActionRoutine); err != nil {
		t.Fatal(err)
	}
	listRequest := pagesRequest(t, "GET", "/?publication=1", ws, user, "OWNER", "")
	listRequest.SetPathValue("slug", pageActionSlug)
	listRequest.SetPathValue("panelId", pageActionPanel)
	listResponse := httptest.NewRecorder()
	h.ListPanelActions(listResponse, listRequest)
	var listed struct {
		Actions []struct {
			Changed *bool `json:"routine_changed_since_publication"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil || listResponse.Code != 200 || len(listed.Actions) != 1 || listed.Actions[0].Changed == nil || !*listed.Actions[0].Changed {
		t.Fatalf("routine drift not visible: %s (%v)", listResponse.Body.String(), err)
	}
	first := dispatch(user, "OWNER", valid, "one")
	if first.Code != 202 {
		t.Fatal(first.Body.String())
	}
	var receipt dispatchReceipt
	json.Unmarshal(first.Body.Bytes(), &receipt)
	again := dispatch(user, "OWNER", valid, "one")
	if again.Code != 202 {
		t.Fatal(again.Body.String())
	}
	var replay dispatchReceipt
	json.Unmarshal(again.Body.Bytes(), &replay)
	if replay.PendingID != receipt.PendingID {
		t.Fatal("retry queued another run")
	}
	if pagesPendingRows(t, h) != 1 {
		t.Fatal("invalid/retried actions enqueued")
	}
	status := func(actor string) *httptest.ResponseRecorder {
		r := pagesRequest(t, "GET", "/", ws, actor, "OWNER", "")
		r.SetPathValue("slug", pageActionSlug)
		r.SetPathValue("pendingId", receipt.PendingID)
		w := httptest.NewRecorder()
		h.ApplicationActionStatus(w, r)
		return w
	}
	if w := status(user); w.Code != 200 {
		t.Fatal(w.Body.String())
	} else {
		var observed struct {
			Changed bool   `json:"routine_changed_since_publication"`
			Digest  string `json:"routine_definition_at_enqueue"`
			Pinned  bool   `json:"routine_revision_pinned"`
		}
		var current string
		if err := h.db.QueryRow(`SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=?`, ws, pageActionRoutine).Scan(&current); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &observed); err != nil || !observed.Changed || observed.Pinned || observed.Digest != pageRoutineDigest(current) {
			t.Fatalf("incorrect routine provenance: %s (%v)", w.Body.String(), err)
		}
	}
	if w := status("other-user"); w.Code != 404 {
		t.Fatalf("receipt leaked %d", w.Code)
	}
	var pageID, spec string
	if err := h.db.QueryRow(`SELECT id,spec_json FROM pages WHERE workspace_id=? AND slug=?`, ws, pageActionSlug).Scan(&pageID, &spec); err != nil {
		t.Fatal(err)
	}
	// Withdrawal between authorization and enqueue must also close the transaction fence.
	if _, err := h.db.Exec(`UPDATE page_project_live SET published=0 WHERE page_id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	withdrawnCtx := context.WithValue(context.Background(), pageApplicationFenceKey{}, &pageApplicationFence{page: pageID, workspace: ws, spec: spec, version: 1})
	if _, _, err := h.enqueuePageAction(withdrawnCtx, pipeline.PendingRun{MetadataJSON: "{}"}); !errors.Is(err, errPageApplicationChanged) {
		t.Fatalf("withdrawal fence failed: %v", err)
	}
	if w := dispatch(user, "OWNER", valid, "withdrawn"); w.Code != 409 {
		t.Fatalf("withdrawn action queued: %d", w.Code)
	}
	if _, err := h.db.Exec(`UPDATE page_project_live SET published=1 WHERE page_id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	// Simulate another editor committing after resolution but before enqueue.
	if _, err := h.db.Exec(`UPDATE pages SET spec_json=spec_json||' ' WHERE id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), pageApplicationFenceKey{}, &pageApplicationFence{page: pageID, workspace: ws, spec: spec, version: 1})
	if _, _, err := h.enqueuePageAction(ctx, pipeline.PendingRun{MetadataJSON: "{}"}); !errors.Is(err, errPageApplicationChanged) {
		t.Fatalf("fence failed: %v", err)
	}
	if w := dispatch(user, "OWNER", valid, "new"); w.Code != 409 {
		t.Fatalf("definition drift %d", w.Code)
	}
	if pagesPendingRows(t, h) != 1 {
		t.Fatal("stale action queued")
	}
}
