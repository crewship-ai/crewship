package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestOneTimeStartPinsAcceptedPublishedRecipe(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	ctx := context.Background()
	now := time.Now()
	in := pipeline.SaveInput{WorkspaceID: ws, Slug: "pin-once", Name: "Pin once", DefinitionJSON: `{"name":"pin-once","steps":[]}`, LastTestRunAt: &now, LastTestRunPassed: true, Author: pipeline.AuthorMeta{UserID: user, Via: pipeline.AuthoredViaUser}}
	p, err := h.store.Save(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	r := withWorkspaceUser(httptest.NewRequest("POST", "/run", nil), user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.enqueueDeferredRun(rr, r, ws, user, p, runRequestBody{FireAt: now.Add(time.Hour).Format(time.RFC3339)})
	if rr.Code != 202 {
		t.Fatalf("enqueue: %d %s", rr.Code, rr.Body)
	}
	in.DefinitionJSON = `{"name":"pin-once","description":"changed","steps":[]}`
	if _, err = h.store.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	pending, err := pipeline.NewPendingRunStore(h.db).DueRuns(ctx, now.Add(2*time.Hour), 10)
	if err != nil || len(pending) != 1 || pending[0].PinnedVersion == nil || *pending[0].PinnedVersion != 1 {
		t.Fatalf("pending=%+v error=%v", pending, err)
	}
	// Atomic authoring also pins the newly archived version.
	if _, _, err = h.store.SaveWithTrigger(ctx, in, &pipeline.TriggerInput{Kind: pipeline.TriggerKindOnce, FireAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = h.db.QueryRow(`SELECT pinned_version FROM pending_runs WHERE id=?`, "pnd_once_"+p.ID).Scan(&version); err != nil || version != 2 {
		t.Fatalf("authoring pin=%d error=%v", version, err)
	}
	// T5: editing the recipe must not revive an explicitly cancelled start.
	if _, err := h.db.Exec(`UPDATE pending_runs SET status='cancelled' WHERE id=?`, "pnd_once_"+p.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = h.store.SaveWithTrigger(ctx, in, &pipeline.TriggerInput{Kind: pipeline.TriggerKindOnce, FireAt: now.Add(2 * time.Hour)})
	if err == nil {
		t.Fatal("T5: cancelled start was rearmed by a recipe save")
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM pending_runs WHERE id=?`, "pnd_once_"+p.ID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("cancelled state lost: %s %v", status, err)
	}
}

func TestT5PendingDispatcherExecutesPinnedArchive(t *testing.T) {
	h, _, ws := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "unused"})
	h.SetRunStore(pipeline.NewRunStore(h.db))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	in := pipeline.SaveInput{WorkspaceID: ws, Slug: "dispatch-pin", Name: "Pinned dispatch", DefinitionJSON: `{"name":"dispatch-pin","steps":[{"id":"result","type":"transform","transform":{"input":"original","expression":"."}}]}`, LastTestRunAt: &now, LastTestRunPassed: true}
	p, err := h.store.Save(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	pending := pipeline.NewPendingRunStore(h.db)
	version := 1
	if _, _, err := pending.Enqueue(ctx, pipeline.PendingRun{ID: "pinned-dispatch", WorkspaceID: ws, PipelineID: p.ID, PipelineSlug: p.Slug, FireAt: now.Add(-time.Minute), PinnedVersion: &version}); err != nil {
		t.Fatal(err)
	}
	in.DefinitionJSON = `{"name":"dispatch-pin","steps":[{"id":"result","type":"transform","transform":{"input":"changed","expression":"."}}]}`
	if _, err := h.store.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	dispatcher := pipeline.NewPendingRunDispatcher(pending, h.newExecutor(), h.logger)
	dispatcher.Start(ctx)
	defer dispatcher.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status, output, hash string
		err := h.db.QueryRow(`SELECT status,output,definition_hash FROM pipeline_runs WHERE pipeline_id=?`, p.ID).Scan(&status, &output, &hash)
		if err == nil && status == "completed" {
			if output != "original" || hash != p.DefinitionHash {
				t.Fatalf("dispatched current recipe: %q %q", output, hash)
			}
			return
		}
		if err == nil && status == "failed" {
			t.Fatalf("pinned run failed: %q", output)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pinned run did not complete")
}

func TestOneTimeStartReportsArchiveReadFailureAsServerError(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	now := time.Now()
	p, err := h.store.Save(context.Background(), pipeline.SaveInput{WorkspaceID: ws, Slug: "db-failure", Name: "Read failure", DefinitionJSON: `{"name":"db-failure","steps":[]}`, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := withWorkspaceUser(httptest.NewRequest("POST", "/run", nil).WithContext(ctx), user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.enqueueDeferredRun(rr, r, ws, user, p, runRequestBody{FireAt: now.Add(time.Hour).Format(time.RFC3339)})
	if rr.Code != 500 {
		t.Fatalf("database failure became publication conflict: %d %s", rr.Code, rr.Body)
	}
}
