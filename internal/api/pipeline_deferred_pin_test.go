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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestEnqueueHelperHonoursACallerPin exercises enqueueDeferredRun directly:
// when the caller has already resolved a pin, the helper does not replace it.
// This is NOT the public contract — the run door refuses pinned_version on a
// delayed or debounced trigger (TestPublicRunDoorRefusesAnExplicitPinOnADeferral);
// the helper's caller today is the one-time start, which resolved the pin
// itself.
func TestEnqueueHelperHonoursACallerPin(t *testing.T) {
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

// No manual deferred start may be acknowledged without an immutable recipe.
func TestDeferredRunRequiresArchive(t *testing.T) {
	for _, body := range []string{
		`{"delay_seconds":3600}`, `{"debounce_key":"k","debounce_window_seconds":3600}`,
		`{"fire_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`,
	} {
		t.Run(body, func(t *testing.T) {
			h, user, ws := newPipelineHandlerForCRUDTest(t)
			h.SetRunner(newBlockingRunner())
			_, p := seedPinnable(t, h, user, ws, "pin-noarchive")
			if _, err := h.db.ExecContext(t.Context(), `DELETE FROM pipeline_versions WHERE pipeline_id=?`, p.ID); err != nil {
				t.Fatal(err)
			}
			req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(body)), user, ws, "OWNER")
			req.SetPathValue("slug", p.Slug)
			rr := httptest.NewRecorder()
			h.Run(rr, req)
			if rr.Code != http.StatusConflict {
				t.Fatalf("without an archive: %d %s, want 409", rr.Code, rr.Body.String())
			}
			var count int
			if err := h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pending_runs`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("refused start stored %d pending rows", count)
			}
		})
	}
}

// presetV1ForCoalesce / presetV2ForCoalesce are one routine across an edit
// that renames its only required input: a preset written for v1 has nothing
// v2 accepts and vice versa. `presetValidationDef` is the v1 shape.
const presetV2ForCoalesce = `{"name":"planned","inputs":[` +
	`{"name":"zone","type":"string","widget":"select","options":["eu","us"],"required":true}],` +
	`"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`

// runPlanned drives the PUBLIC run door for the "planned" routine.
func runPlanned(t *testing.T, h *PipelineHandler, user, ws, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", bytes.NewBufferString(body)), user, ws, "OWNER")
	req.SetPathValue("slug", "planned")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	return rr
}

// TestCoalesceRefusesInputsThePinnedRecipeRejects — opponent round 2. The
// first trigger opened a window on v1 with {"region":"eu"}; v2 renames the
// input; a second trigger in the window sends {"zone":"us"}, valid for HEAD.
// Keeping the pin (correct) and adopting the inputs (correct) would store a
// row v1 cannot run. The pair is judged together and this request is
// refused, leaving the first trigger's row exactly as accepted.
func TestCoalesceRefusesInputsThePinnedRecipeRejects(t *testing.T) {
	h, user, ws := presetRig(t)
	h.SetRunner(newBlockingRunner())
	p := seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)

	first := runPlanned(t, h, user, ws, `{"inputs":{"region":"eu"},"debounce_key":"same","debounce_window_seconds":3600}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	now := time.Now()
	if _, err := h.store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: ws, Slug: "planned", Name: "Planned",
		DefinitionJSON: presetV2ForCoalesce, LastTestRunAt: &now, LastTestRunPassed: true}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	second := runPlanned(t, h, user, ws, `{"inputs":{"zone":"us"},"debounce_key":"same","debounce_window_seconds":3600}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("incompatible coalesce: %d %s, want 409", second.Code, second.Body.String())
	}
	for _, want := range []string{"version 1", "region"} {
		if !bytes.Contains(second.Body.Bytes(), []byte(want)) {
			t.Errorf("refusal %s does not name %q", second.Body.String(), want)
		}
	}

	rows, err := pipeline.NewPendingRunStore(h.db).DueRuns(t.Context(), time.Now().Add(2*time.Hour), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	row := rows[0]
	if row.PinnedVersion == nil || *row.PinnedVersion != 1 || row.InputsJSON != `{"region":"eu"}` {
		t.Fatalf("row changed by a refused request: pin=%v inputs=%s", row.PinnedVersion, row.InputsJSON)
	}
	v, err := h.store.GetVersion(t.Context(), p.ID, *row.PinnedVersion)
	if err != nil {
		t.Fatal(err)
	}
	dsl, err := pipeline.Parse([]byte(v.DefinitionJSON))
	if err != nil {
		t.Fatal(err)
	}
	var inputs map[string]any
	if err := json.Unmarshal([]byte(row.InputsJSON), &inputs); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ValidateFormInputs(dsl, inputs); err != nil {
		t.Fatalf("stored pair is not runnable: pin=%d inputs=%s: %v", *row.PinnedVersion, row.InputsJSON, err)
	}

}

// presetV2Additive is presetValidationDef plus one optional input: a request
// written for it is valid for v1 too, because v1 does not declare the key.
const presetV2Additive = `{"name":"planned","inputs":[` +
	`{"name":"region","type":"string","widget":"select","options":["eu","us"],"required":true},` +
	`{"name":"dry_run","type":"boolean","widget":"boolean","default":true},` +
	`{"name":"verbose","type":"boolean","widget":"boolean","default":false}],` +
	`"steps":[{"id":"a","type":"transform","transform":{"input":"hi","expression":"."}}]}`

// TestCoalesceKeepsAcceptingCompatibleInputs — the other half of the rule.
// A payload the kept pin CAN run still coalesces: inputs and attribution
// move to the row, the pin stays, and the receipt reports the stored pair.
// Note the request must satisfy HEAD as well (the run door's preflight is
// unchanged), so this is an additive edit rather than a rename.
func TestCoalesceKeepsAcceptingCompatibleInputs(t *testing.T) {
	h, user, ws := presetRig(t)
	h.SetRunner(newBlockingRunner())
	seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)

	first := runPlanned(t, h, user, ws, `{"inputs":{"region":"eu"},"debounce_key":"same","debounce_window_seconds":3600}`)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	var opened map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := h.store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: ws, Slug: "planned", Name: "Planned",
		DefinitionJSON: presetV2Additive, LastTestRunAt: &now, LastTestRunPassed: true}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	other := seedMemberWithCapabilities(t, h.db, ws, "OWNER", "[]", "coalesce-other")

	second := runPlanned(t, h, other, ws, `{"inputs":{"region":"us","verbose":true},"debounce_key":"same","debounce_window_seconds":3600}`)
	if second.Code != http.StatusAccepted {
		t.Fatalf("compatible coalesce: %d %s", second.Code, second.Body.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["coalesced"] != true || receipt["pinned_version"] != float64(1) || receipt["pending_id"] != opened["pending_id"] {
		t.Fatalf("receipt %v, want coalesced into %v on pin 1", receipt, opened["pending_id"])
	}
	rows, err := pipeline.NewPendingRunStore(h.db).DueRuns(t.Context(), time.Now().Add(2*time.Hour), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	row := rows[0]
	var inputs map[string]any
	if err := json.Unmarshal([]byte(row.InputsJSON), &inputs); err != nil {
		t.Fatal(err)
	}
	if inputs["region"] != "us" || inputs["verbose"] != true || row.PinnedVersion == nil || *row.PinnedVersion != 1 || row.InvokingUserID != other {
		t.Fatalf("stored pin=%v inputs=%s user=%s, want the second payload under its own user on pin 1", row.PinnedVersion, row.InputsJSON, row.InvokingUserID)
	}
}

// TestPublicRunDoorRefusesAnExplicitPinOnADeferral — the contract the docs
// must state: `pinned_version` is honoured for an immediate or one-time
// start only. A delayed or debounced trigger pins what was published when
// it was accepted, and naming a version is a 400, not a silent override.
func TestPublicRunDoorRefusesAnExplicitPinOnADeferral(t *testing.T) {
	h, user, ws := presetRig(t)
	h.SetRunner(newBlockingRunner())
	seedRoutineForPreset(t, h, ws, "planned", presetValidationDef)
	for _, body := range []string{
		`{"inputs":{"region":"eu"},"pinned_version":1,"delay_seconds":60}`,
		`{"inputs":{"region":"eu"},"pinned_version":1,"debounce_key":"k"}`,
	} {
		rr := runPlanned(t, h, user, ws, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s → %d %s, want 400", body, rr.Code, rr.Body.String())
		}
	}
}

func TestDeferredReceiptReportsDebounceCap(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	_, p := seedPinnable(t, h, user, ws, "receipt-cap")
	first := enqueueDelayed(t, h, user, ws, p, runRequestBody{DebounceKey: "k", DebounceWindowSecond: 10, DebounceMaxSeconds: 60})
	second := enqueueDelayed(t, h, user, ws, p, runRequestBody{DebounceKey: "k", DebounceWindowSecond: 3600})
	var fireAt, maxAt string
	if err := h.db.QueryRowContext(t.Context(), `SELECT fire_at,debounce_max_at FROM pending_runs WHERE id=?`, first["pending_id"]).Scan(&fireAt, &maxAt); err != nil {
		t.Fatal(err)
	}
	if fireAt != maxAt || second["fire_at"] != fireAt {
		t.Fatalf("receipt=%v stored=%s cap=%s", second["fire_at"], fireAt, maxAt)
	}
}
