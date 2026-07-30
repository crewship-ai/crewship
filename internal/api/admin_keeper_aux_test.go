package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

func newKeeperAuxHandler(t *testing.T) (*AdminKeeperAuxHandler, *keepercfg.AuxStore) {
	t.Helper()
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (id, email, full_name)
		VALUES ('admin-1', 'admin@example.com', 'Admin')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	aux := keepercfg.NewAuxStore(db, llm.DefaultAuxiliaryModels())
	if err := aux.Load(context.Background()); err != nil {
		t.Fatalf("load aux store: %v", err)
	}
	judge := keepercfg.New(db, keeperEnvDefaults)
	if err := judge.Load(context.Background()); err != nil {
		t.Fatalf("load judge store: %v", err)
	}
	return NewAdminKeeperAuxHandler(aux, judge, nil, newTestLogger()), aux
}

func slotReq(method, slot, body string) *http.Request {
	req := manageReq(method, "/api/v1/admin/keeper/aux/"+slot, body)
	req.SetPathValue("slot", slot)
	return req
}

func decodeAux(t *testing.T, rr *httptest.ResponseRecorder) keeperAuxResponse {
	t.Helper()
	var resp keeperAuxResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	return resp
}

func auxRow(t *testing.T, resp keeperAuxResponse, slot string) keeperAuxSlotResponse {
	t.Helper()
	for _, s := range resp.Slots {
		if s.Slot == slot {
			return s
		}
	}
	t.Fatalf("slot %q missing from response", slot)
	return keeperAuxSlotResponse{}
}

func TestAdminKeeperAux_RequiresManageRole(t *testing.T) {
	h, _ := newKeeperAuxHandler(t)

	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		verb string
	}{
		{"get", h.Get, "GET"},
		{"put", h.Put, "PUT"},
		{"reset", h.Reset, "DELETE"},
		{"use-judge", h.UseJudge, "POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.verb, "/api/v1/admin/keeper/aux", strings.NewReader("{}"))
			req = req.WithContext(context.WithValue(req.Context(), ctxRole, "VIEWER"))
			rr := httptest.NewRecorder()
			tc.fn(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("viewer got %d, want 403", rr.Code)
			}
		})
	}
}

// An unwired store must not render as "nothing overridden" — that is an editable
// form whose saves vanish.
func TestAdminKeeperAux_UnwiredStoreIs503(t *testing.T) {
	h := NewAdminKeeperAuxHandler(nil, nil, nil, newTestLogger())
	rr := httptest.NewRecorder()
	h.Get(rr, manageReq("GET", "/api/v1/admin/keeper/aux", ""))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rr.Code)
	}
}

func TestAdminKeeperAux_GetListsEverySlotWithProvenance(t *testing.T) {
	h, _ := newKeeperAuxHandler(t)

	rr := httptest.NewRecorder()
	h.Get(rr, manageReq("GET", "/api/v1/admin/keeper/aux", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	resp := decodeAux(t, rr)

	if len(resp.Slots) != len(keepercfg.AuxSlots) {
		t.Fatalf("got %d slots, want %d", len(resp.Slots), len(keepercfg.AuxSlots))
	}
	if resp.AnyOverridden {
		t.Error("any_overridden = true with nothing stored")
	}
	// The picker must not hardcode the provider vocabulary — it is served, and it
	// is narrower than the model catalogue.
	if len(resp.Providers) == 0 {
		t.Error("no providers served; the console would have to hardcode them")
	}
	for _, p := range resp.Providers {
		if p == "google" {
			t.Error("google is offered but no Gemini provider can be built")
		}
	}
	// The bulk action needs the judge's model, or the console cannot tell a
	// disabled button from a 400.
	if resp.JudgeModel != keeperEnvDefaults.Model {
		t.Errorf("judge_model = %q, want %q", resp.JudgeModel, keeperEnvDefaults.Model)
	}

	behavior := auxRow(t, resp, string(llm.SlotBehavior))
	if behavior.Model.Value != "claude-haiku-4-5" || behavior.Model.Source != "default" {
		t.Errorf("behavior model = %q/%s, want the shipped default", behavior.Model.Value, behavior.Model.Source)
	}
	if !behavior.Model.Editable {
		t.Error("behavior model is not editable — the whole point of the endpoint")
	}
	if behavior.AppliesAt != keepercfg.AppliesImmediately {
		t.Errorf("behavior applies_at = %q, want immediately", behavior.AppliesAt)
	}
	// run_summary is captured into the pipeline executors at boot; saying
	// otherwise would have an operator conclude the write failed.
	if run := auxRow(t, resp, string(llm.SlotRunSummary)); run.AppliesAt != keepercfg.AppliesOnRestart {
		t.Errorf("run_summary applies_at = %q, want restart", run.AppliesAt)
	}
}

func TestAdminKeeperAux_PutOverridesOneSlot(t *testing.T) {
	h, store := newKeeperAuxHandler(t)

	rr := httptest.NewRecorder()
	h.Put(rr, slotReq("PUT", string(llm.SlotCurator),
		`{"provider":"anthropic","model":"claude-opus-5","timeout_ms":45000}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	resp := decodeAux(t, rr)

	curator := auxRow(t, resp, string(llm.SlotCurator))
	if curator.Model.Value != "claude-opus-5" || curator.Model.Source != "instance" {
		t.Errorf("model = %q/%s, want claude-opus-5/instance", curator.Model.Value, curator.Model.Source)
	}
	if curator.TimeoutMS.Value != 45000 {
		t.Errorf("timeout = %d, want 45000", curator.TimeoutMS.Value)
	}
	if !resp.AnyOverridden {
		t.Error("any_overridden = false after an override")
	}
	// Opus 5 is the model an operator most often reaches for here, so a picker
	// that cannot offer it makes the endpoint useless — pin that it is in the
	// catalogue the console renders.
	found := false
	for _, m := range llm.CuratedModels("anthropic") {
		if m.ID == "claude-opus-5" {
			found = true
		}
	}
	if !found {
		t.Error("claude-opus-5 is absent from the curated Anthropic catalogue the picker renders")
	}
	// And the write reached the config the evaluators resolve from.
	if got := store.Resolved().Curator.Model; got != "claude-opus-5" {
		t.Errorf("resolved curator model = %q", got)
	}
	// Unrelated slots are untouched.
	if got := store.Resolved().Behavior.Model; got != "claude-haiku-4-5" {
		t.Errorf("an unrelated slot changed: %q", got)
	}
}

func TestAdminKeeperAux_PutRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		slot string
		body string
		want string
	}{
		{"unknown slot", "not_a_slot", `{"model":"claude-opus-5"}`, "unknown evaluator slot"},
		{"unbuildable provider", string(llm.SlotCurator), `{"provider":"google","model":"gemini-2.0-flash"}`, "Gemini"},
		{"provider without a model", string(llm.SlotCurator), `{"provider":"anthropic"}`, "needs a model"},
		{"absurd timeout", string(llm.SlotCurator), `{"timeout_ms":9000000}`, "at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newKeeperAuxHandler(t)
			rr := httptest.NewRecorder()
			h.Put(rr, slotReq("PUT", tc.slot, tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.want) {
				t.Errorf("body %q does not mention %q", rr.Body.String(), tc.want)
			}
			// A refused write must leave the slot inheriting, not half-applied.
			if got := store.Resolved().Curator.Model; got != "claude-haiku-4-5" {
				t.Errorf("a rejected write changed the resolved model to %q", got)
			}
		})
	}
}

func TestAdminKeeperAux_PutMissingSlotIs400(t *testing.T) {
	h, _ := newKeeperAuxHandler(t)
	rr := httptest.NewRecorder()
	h.Put(rr, manageReq("PUT", "/api/v1/admin/keeper/aux/", `{"model":"claude-opus-5"}`))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rr.Code)
	}
}

func TestAdminKeeperAux_UseJudgePointsEverySlotAtTheLocalModel(t *testing.T) {
	h, store := newKeeperAuxHandler(t)

	rr := httptest.NewRecorder()
	h.UseJudge(rr, manageReq("POST", "/api/v1/admin/keeper/aux/use-judge", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	resp := decodeAux(t, rr)

	for _, s := range resp.Slots {
		if s.Provider.Value != "ollama" || s.Model.Value != keeperEnvDefaults.Model {
			t.Errorf("slot %s = %s/%s, want the local judge", s.Slot, s.Provider.Value, s.Model.Value)
		}
		if !s.Overridden {
			t.Errorf("slot %s is not marked overridden", s.Slot)
		}
	}
	if got := store.Resolved().MemoryHealth.Provider; got != "ollama" {
		t.Errorf("resolved provider = %q, want ollama", got)
	}
}

// Pointing the evaluators at a judge that has no model would write a provider
// with no model — the one combination that cannot resolve.
func TestAdminKeeperAux_UseJudgeRefusesWithoutAConfiguredJudge(t *testing.T) {
	db := setupTestDB(t)
	aux := keepercfg.NewAuxStore(db, llm.DefaultAuxiliaryModels())
	if err := aux.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	judge := keepercfg.New(db, keepercfg.Defaults{}) // nothing configured
	if err := judge.Load(context.Background()); err != nil {
		t.Fatalf("load judge: %v", err)
	}
	h := NewAdminKeeperAuxHandler(aux, judge, nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.UseJudge(rr, manageReq("POST", "/api/v1/admin/keeper/aux/use-judge", ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "judge endpoint") {
		t.Errorf("body %q does not say what to fix", rr.Body.String())
	}
}

func TestAdminKeeperAux_ResetOneSlotAndAll(t *testing.T) {
	h, store := newKeeperAuxHandler(t)
	rr := httptest.NewRecorder()
	h.UseJudge(rr, manageReq("POST", "/api/v1/admin/keeper/aux/use-judge", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.Reset(rr, slotReq("DELETE", string(llm.SlotBehavior), ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("reset one: %d %s", rr.Code, rr.Body.String())
	}
	resp := decodeAux(t, rr)
	if auxRow(t, resp, string(llm.SlotBehavior)).Overridden {
		t.Error("the reset slot is still overridden")
	}
	if !auxRow(t, resp, string(llm.SlotCurator)).Overridden {
		t.Error("resetting one slot cleared another")
	}

	// The collection-scoped DELETE carries no {slot} — that is "reset every slot".
	rr = httptest.NewRecorder()
	h.Reset(rr, manageReq("DELETE", "/api/v1/admin/keeper/aux", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("reset all: %d %s", rr.Code, rr.Body.String())
	}
	resp = decodeAux(t, rr)
	if resp.AnyOverridden {
		t.Error("any_overridden = true after a full reset")
	}
	if got := store.Resolved().Curator.Model; got != "claude-haiku-4-5" {
		t.Errorf("resolved model after reset = %q, want the inherited default", got)
	}
}
