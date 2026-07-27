package api

// The aux-status surface used to echo configuration: it printed whatever
// llm.AuxiliaryModels held and called it "explicit". That is not what an
// operator asking "is the keeper working?" needs, and on a real box it was
// actively misleading:
//
//   - It listed a `keeper` slot. Nothing in the codebase ever calls
//     ResolveAux(cfg, SlotKeeper) — the credential-access gatekeeper is
//     built from cfg.Keeper (Ollama) in server.go, a completely separate
//     config path that happens to share the word. So the card showed
//     "keeper: anthropic/claude-haiku-4-5" while the real judge ran
//     ollama/qwen2.5:7b.
//
//   - It never said whether the configured provider could actually be
//     built. A slot pointing at anthropic with no ANTHROPIC_API_KEY in the
//     process env falls back to a local judge at boot; the card reported
//     "explicit" either way.
//
// These tests pin the surface to effective state.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/llm"
)

func auxStatusBody(t *testing.T, h *AuxStatusHandler) auxStatusResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/aux-status?workspace_id=ws1", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out auxStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v — body %s", err, rr.Body.String())
	}
	return out
}

func rowByID(rows []auxSubsystemRow, id string) (auxSubsystemRow, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return auxSubsystemRow{}, false
}

func TestAuxStatus_DropsTheSlotNothingConsumes(t *testing.T) {
	h := NewAuxStatusHandler(llm.DefaultAuxiliaryModels(), nil, newTestLogger())
	out := auxStatusBody(t, h)

	// Listing it invited an operator to configure a slot that changes
	// nothing, and to read the real keeper's model off the wrong row.
	if _, found := rowByID(out.Subsystems, "keeper"); found {
		t.Error("aux-status still lists the `keeper` slot; nothing calls ResolveAux for it")
	}
	for _, id := range []string{"curator", "behavior", "memory_health", "negative"} {
		if _, found := rowByID(out.Subsystems, id); !found {
			t.Errorf("slot %q missing — it has real consumers and must stay", id)
		}
	}
}

func TestAuxStatus_ReportsTheRealCredentialJudge(t *testing.T) {
	keeper := &config.KeeperConfig{Enabled: true, OllamaURL: "http://127.0.0.1:11434", Model: "qwen2.5:7b"}
	h := NewAuxStatusHandler(llm.DefaultAuxiliaryModels(), keeper, newTestLogger())
	out := auxStatusBody(t, h)

	row, found := rowByID(out.Subsystems, "access_gatekeeper")
	if !found {
		t.Fatal("the credential-access judge is missing — it is the one an operator most needs to see")
	}
	// It comes from cfg.Keeper, NOT from an aux slot. Reporting it from the
	// aux config is exactly the confusion this surface caused before.
	if row.Model != "qwen2.5:7b" || row.Provider != "ollama" {
		t.Errorf("access judge = %s/%s, want ollama/qwen2.5:7b from cfg.Keeper", row.Provider, row.Model)
	}
}

func TestAuxStatus_SaysWhenTheAccessJudgeIsSwitchedOff(t *testing.T) {
	keeper := &config.KeeperConfig{Enabled: false, OllamaURL: "http://127.0.0.1:11434", Model: "qwen2.5:7b"}
	h := NewAuxStatusHandler(llm.DefaultAuxiliaryModels(), keeper, newTestLogger())
	out := auxStatusBody(t, h)

	row, found := rowByID(out.Subsystems, "access_gatekeeper")
	if !found {
		t.Fatal("row should still be present when disabled — absence reads as 'fine'")
	}
	if row.Healthy {
		t.Error("a disabled access judge must not report healthy")
	}
	if row.Detail == "" {
		t.Error("a not-healthy row must say why; a bare red dot is not actionable")
	}
}

func TestAuxStatus_FlagsASlotWhoseProviderCannotBeBuilt(t *testing.T) {
	// No ANTHROPIC_API_KEY in the test process env, so an anthropic slot
	// cannot be built — the same condition that makes the server fall back
	// to a local judge at boot. The card used to call this "explicit".
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := llm.AuxiliaryModels{
		Curator:  llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
		Fallback: llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
	}
	h := NewAuxStatusHandler(cfg, nil, newTestLogger())
	out := auxStatusBody(t, h)

	row, found := rowByID(out.Subsystems, "curator")
	if !found {
		t.Fatal("curator row missing")
	}
	if row.Healthy {
		t.Error("a slot whose provider cannot be built must not report healthy")
	}
	if row.Detail == "" {
		t.Error("the reason the provider could not be built is the whole point of the row")
	}
}

func TestAuxStatus_HealthyWhenTheProviderBuilds(t *testing.T) {
	// ollama needs no key, so it always builds.
	cfg := llm.AuxiliaryModels{
		Curator:  llm.AuxModel{Provider: "ollama", Model: "qwen2.5:7b"},
		Fallback: llm.AuxModel{Provider: "ollama", Model: "qwen2.5:7b"},
	}
	h := NewAuxStatusHandler(cfg, nil, newTestLogger())
	out := auxStatusBody(t, h)

	row, _ := rowByID(out.Subsystems, "curator")
	if !row.Healthy {
		t.Errorf("buildable slot reported unhealthy: %+v", row)
	}
	if row.Detail != "" {
		t.Errorf("healthy row should not carry a problem detail, got %q", row.Detail)
	}
}

func TestAuxStatus_StillRequiresAuth(t *testing.T) {
	h := NewAuxStatusHandler(llm.DefaultAuxiliaryModels(), nil, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/aux-status?workspace_id=ws1", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without a user in context", rr.Code)
	}
}
