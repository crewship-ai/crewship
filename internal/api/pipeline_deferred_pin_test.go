package api

// #2500 — a deferred run must execute the recipe it was accepted against.
//
// `POST .../run` with `delay_seconds` parks the trigger in pending_runs and
// answers 202 SCHEDULED with a handle. That run was accepted: it passed the
// governance status gate, the integrations, resources and credentials
// preconditions, and the caller was handed a receipt for it. What it must
// not do is come back minutes later running a recipe that did not exist
// when they pressed Run.
//
// The pin already existed for the one-time SCHEDULED START form
// (TestOneTimeStartPinsAcceptedPublishedRecipe) and was gated on FireAt, so
// the delay form took the same pending_runs path with pinned_version NULL.
// The comment on that branch — "pin the definition that passed preflight,
// not a concurrently changed HEAD" — is the whole argument, and it does not
// mention which shape of deferral the caller used.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// seedPinnable saves a routine and returns it, ready to be re-saved to mint
// a second archived version.
func seedPinnable(t *testing.T, h *PipelineHandler, user, ws, slug string) (pipeline.SaveInput, *pipeline.Pipeline) {
	t.Helper()
	now := time.Now()
	in := pipeline.SaveInput{
		WorkspaceID: ws, Slug: slug, Name: slug,
		DefinitionJSON:    `{"name":"` + slug + `","steps":[]}`,
		LastTestRunAt:     &now,
		LastTestRunPassed: true,
		Author:            pipeline.AuthorMeta{UserID: user, Via: pipeline.AuthoredViaUser},
	}
	p, err := h.store.Save(t.Context(), in)
	if err != nil {
		t.Fatalf("seed routine: %v", err)
	}
	return in, p
}

// enqueueDelayed drives the manual run door's deferral and returns the
// receipt it wrote.
func enqueueDelayed(t *testing.T, h *PipelineHandler, user, ws string, p *pipeline.Pipeline, body runRequestBody) map[string]any {
	t.Helper()
	r := withWorkspaceUser(httptest.NewRequest("POST", "/run", nil), user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.enqueueDeferredRun(rr, r, ws, user, p, body)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %s", rr.Code, rr.Body.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v (%s)", err, rr.Body.String())
	}
	return receipt
}

func pendingPin(t *testing.T, h *PipelineHandler, when time.Time) *int {
	t.Helper()
	pending, err := pipeline.NewPendingRunStore(h.db).DueRuns(t.Context(), when, 10)
	if err != nil {
		t.Fatalf("due runs: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending rows = %d, want 1", len(pending))
	}
	return pending[0].PinnedVersion
}

// TestDelayedRunPinsTheAcceptedRecipe is the defect.
func TestDelayedRunPinsTheAcceptedRecipe(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	in, p := seedPinnable(t, h, user, ws, "pin-delay")

	receipt := enqueueDelayed(t, h, user, ws, p, runRequestBody{DelaySeconds: 3600})
	if receipt["pinned_version"] == nil {
		t.Error("the receipt reports no pinned version, so the caller cannot tell what they scheduled")
	}

	// HEAD moves while the run waits.
	in.DefinitionJSON = `{"name":"pin-delay","description":"published while the run waited","steps":[]}`
	if _, err := h.store.Save(t.Context(), in); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	pin := pendingPin(t, h, time.Now().Add(2*time.Hour))
	if pin == nil {
		t.Fatal("the queued run is not pinned, so it will execute whatever HEAD has become")
	}
	if *pin != 1 {
		t.Errorf("pinned_version = %d, want 1 — the version that was published when the run was accepted", *pin)
	}
}

// TestDebouncedRunPinsTheFirstAcceptedRecipe — a debounce window coalesces a
// burst into one logical trigger, so the pin belongs to the trigger that
// opened the window rather than to whatever HEAD is when it finally fires.
func TestDebouncedRunPinsTheFirstAcceptedRecipe(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	in, p := seedPinnable(t, h, user, ws, "pin-debounce")

	enqueueDelayed(t, h, user, ws, p, runRequestBody{DebounceKey: "k", DebounceWindowSecond: 3600})
	in.DefinitionJSON = `{"name":"pin-debounce","description":"v2","steps":[]}`
	if _, err := h.store.Save(t.Context(), in); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	// The second trigger arrives the way a real one does: the handler loads
	// the routine fresh, so it sees v2. The first version of this test
	// re-passed the stale v1 object here, which made the pin lookup find v1
	// again and hid that the coalesce UPDATE overwrites pinned_version — a
	// probe that cannot fail is not a probe.
	current, err := h.store.GetByID(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("reload routine: %v", err)
	}
	if current.DefinitionHash == p.DefinitionHash {
		t.Fatal("fixture: v2 did not change the definition hash")
	}
	second := enqueueDelayed(t, h, user, ws, current, runRequestBody{DebounceKey: "k", DebounceWindowSecond: 3600})
	// The receipt for the coalescing trigger must say what the ROW carries.
	// Found live: the row kept 1 while the receipt said 2 — the caller
	// would have been told the wrong recipe for the run that fires.
	if second["coalesced"] != true {
		t.Fatalf("second trigger did not coalesce: %v", second)
	}
	if got, _ := second["pinned_version"].(float64); got != 1 {
		t.Errorf("coalesced receipt reports pinned_version %v, want 1 — the row's pin, not this request's", second["pinned_version"])
	}

	pin := pendingPin(t, h, time.Now().Add(2*time.Hour))
	if pin == nil {
		t.Fatal("pinned_version = nil, want 1")
	}
	if *pin != 1 {
		t.Errorf("pinned_version = %d, want 1 — a coalesced burst is one trigger, accepted against v1; the coalesce must not take the later trigger's pin", *pin)
	}
}

// TestOneTimeStartStillPins is the behaviour that already worked, kept here
// so widening the condition cannot quietly drop it.
func TestOneTimeStartStillPins(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	in, p := seedPinnable(t, h, user, ws, "pin-fireat")
	now := time.Now()

	enqueueDelayed(t, h, user, ws, p, runRequestBody{FireAt: now.Add(time.Hour).Format(time.RFC3339)})
	in.DefinitionJSON = `{"name":"pin-fireat","description":"v2","steps":[]}`
	if _, err := h.store.Save(t.Context(), in); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	pin := pendingPin(t, h, now.Add(2*time.Hour))
	if pin == nil || *pin != 1 {
		t.Errorf("pinned_version = %v, want 1", pin)
	}
}

// TestExplicitPinIsNotOverridden — a caller that names a version keeps it.
func TestExplicitPinIsNotOverridden(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	in, p := seedPinnable(t, h, user, ws, "pin-explicit")
	in.DefinitionJSON = `{"name":"pin-explicit","description":"v2","steps":[]}`
	if _, err := h.store.Save(t.Context(), in); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	one := 1
	enqueueDelayed(t, h, user, ws, p, runRequestBody{DelaySeconds: 3600, PinnedVersion: &one})
	pin := pendingPin(t, h, time.Now().Add(2*time.Hour))
	if pin == nil || *pin != 1 {
		t.Errorf("pinned_version = %v, want the caller's explicit 1", pin)
	}
}

// TestDeferredRunWithoutAnArchiveStillEnqueues is the compatibility line. A
// routine old enough to have no pipeline_versions row can still be deferred;
// it runs unpinned and the receipt says so, rather than the caller losing a
// run that works today. The one-time start keeps its refusal, because for
// that form the pin IS the feature.
func TestDeferredRunWithoutAnArchiveStillEnqueues(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	_, p := seedPinnable(t, h, user, ws, "pin-noarchive")
	if _, err := h.db.ExecContext(t.Context(), `DELETE FROM pipeline_versions WHERE pipeline_id=?`, p.ID); err != nil {
		t.Fatalf("drop archive: %v", err)
	}

	receipt := enqueueDelayed(t, h, user, ws, p, runRequestBody{DelaySeconds: 3600})
	if receipt["pinned_version"] != nil {
		t.Errorf("pinned_version = %v, want null when there is nothing to pin", receipt["pinned_version"])
	}
	if pin := pendingPin(t, h, time.Now().Add(2*time.Hour)); pin != nil {
		t.Errorf("pinned_version = %v, want nil", *pin)
	}

	// The one-time start still refuses, unchanged.
	r := withWorkspaceUser(httptest.NewRequest("POST", "/run", nil), user, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.enqueueDeferredRun(rr, r, ws, user, p, runRequestBody{FireAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
	if rr.Code != http.StatusConflict {
		t.Errorf("one-time start without an archive: %d %s, want 409", rr.Code, rr.Body.String())
	}
}
