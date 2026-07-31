package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/llm"
)

func newAuxStatusHandler(cfg llm.AuxiliaryModels) *AuxStatusHandler {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewAuxStatusHandler(auxStatic(cfg), nil, logger)
}

// auxStatic pins the aux config for a test. Production passes
// Router.AuxModels, which re-reads the override store per request (#1556).
func auxStatic(cfg llm.AuxiliaryModels) func() llm.AuxiliaryModels {
	return func() llm.AuxiliaryModels { return cfg }
}

func TestAuxStatus_Unauthorized(t *testing.T) {
	t.Parallel()
	h := newAuxStatusHandler(llm.DefaultAuxiliaryModels())

	req := httptest.NewRequest("GET", "/api/v1/system/aux-status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestAuxStatus_HappyPath_DefaultsAllSlots(t *testing.T) {
	t.Parallel()
	h := newAuxStatusHandler(llm.DefaultAuxiliaryModels())

	req := httptest.NewRequest("GET", "/api/v1/system/aux-status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u-1", Email: "t@x"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp auxStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// MVP defaults: 6 explicit slots, every one on anthropic/claude-haiku-4-5
	// (Fallback is NOT in the slot list — it's an internal backstop).
	// 5 consumed aux slots + the credential-access judge. `keeper` is
	// gone: nothing calls ResolveAux for it (see system_aux_effective_test.go).
	if got, want := len(resp.Subsystems), 6; got != want {
		t.Fatalf("slot count = %d, want %d", got, want)
	}
	// The access judge leads (it is what operators come here for), then the
	// aux slots in llm.AuxiliaryModels field order.
	wantOrder := []string{"access_gatekeeper", "curator", "behavior", "memory_health", "negative", "run_summary"}
	for i, row := range resp.Subsystems {
		if row.ID != wantOrder[i] {
			t.Errorf("slots[%d].Slot = %q, want %q", i, row.ID, wantOrder[i])
		}
		if row.ID == "access_gatekeeper" {
			// Sourced from cfg.Keeper, not from the aux config under test.
			continue
		}
		if row.Provider != "anthropic" {
			t.Errorf("slots[%d].Provider = %q, want anthropic", i, row.Provider)
		}
		if row.Model != "claude-haiku-4-5" {
			t.Errorf("slots[%d].Model = %q, want claude-haiku-4-5", i, row.Model)
		}
		if row.Source != "explicit" {
			t.Errorf("slots[%d].Source = %q, want explicit (default cfg sets every slot)", i, row.Source)
		}
		if row.TimeoutMS <= 0 {
			t.Errorf("slots[%d].TimeoutMS = %d, want >0", i, row.TimeoutMS)
		}
	}
}

func TestAuxStatus_FallbackSource(t *testing.T) {
	t.Parallel()
	// Only Fallback configured; every slot must resolve to Fallback
	// with source="fallback".
	cfg := llm.AuxiliaryModels{
		Fallback: llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: 9 * time.Second},
	}
	h := newAuxStatusHandler(cfg)

	req := httptest.NewRequest("GET", "/api/v1/system/aux-status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u-1"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp auxStatusResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	for _, row := range resp.Subsystems {
		if row.ID == "access_gatekeeper" {
			continue // different config path; covered in system_aux_effective_test.go
		}
		if row.Source != "fallback" {
			t.Errorf("slot %q source = %q, want fallback (only Fallback configured)", row.ID, row.Source)
		}
		if row.TimeoutMS != 9000 {
			t.Errorf("slot %q TimeoutMS = %d, want 9000 (fallback timeout)", row.ID, row.TimeoutMS)
		}
	}
}

func TestAuxStatus_MixedExplicitAndFallback(t *testing.T) {
	t.Parallel()
	// Keeper has its own explicit slot; everything else falls back.
	cfg := llm.AuxiliaryModels{
		Keeper:   llm.AuxModel{Provider: "ollama", Model: "phi3:mini", Timeout: 3 * time.Second},
		Fallback: llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5", Timeout: 10 * time.Second},
	}
	h := newAuxStatusHandler(cfg)

	req := httptest.NewRequest("GET", "/api/v1/system/aux-status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u-1"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp auxStatusResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	for _, row := range resp.Subsystems {
		if row.ID == "access_gatekeeper" {
			continue // different config path; covered in system_aux_effective_test.go
		}
		switch row.ID {
		case "keeper":
			if row.Source != "explicit" {
				t.Errorf("keeper source = %q, want explicit", row.Source)
			}
			if row.Provider != "ollama" || row.Model != "phi3:mini" {
				t.Errorf("keeper provider/model = %s/%s, want ollama/phi3:mini", row.Provider, row.Model)
			}
		default:
			if row.Source != "fallback" {
				t.Errorf("%s source = %q, want fallback", row.ID, row.Source)
			}
			if row.Provider != "anthropic" {
				t.Errorf("%s provider = %q, want anthropic (fallback)", row.ID, row.Provider)
			}
		}
	}
}

func TestAuxStatus_UnconfiguredWhenSlotAndFallbackEmpty(t *testing.T) {
	t.Parallel()
	// Zero-valued AuxiliaryModels — neither slot nor fallback has a
	// provider. ResolveAux returns an error per slot; the handler
	// surfaces it as source="unconfigured" so partial diagnostics
	// still render rather than 500ing the whole status page.
	h := newAuxStatusHandler(llm.AuxiliaryModels{})

	req := httptest.NewRequest("GET", "/api/v1/system/aux-status", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u-1"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp auxStatusResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)

	if len(resp.Subsystems) != 6 {
		t.Fatalf("subsystem count = %d, want 6 (5 aux slots + access judge)", len(resp.Subsystems))
	}
	for _, row := range resp.Subsystems {
		if row.ID == "access_gatekeeper" {
			continue // different config path; covered in system_aux_effective_test.go
		}
		if row.Source != "unconfigured" {
			t.Errorf("slot %q source = %q, want unconfigured", row.ID, row.Source)
		}
		if row.Provider != "" || row.Model != "" {
			t.Errorf("slot %q should be blank when unconfigured; got %s/%s", row.ID, row.Provider, row.Model)
		}
	}
}

func TestRouter_AuxModels_DefaultsWhenUnset(t *testing.T) {
	t.Parallel()
	// When WithAuxiliaryModels was not passed, AuxModels() returns
	// the MVP defaults rather than a zero-valued struct — this is
	// what keeps the aux-status endpoint useful in test/dev builds
	// (and what prevents PR-C evaluators from blowing up on a zero-
	// valued struct that would fail ResolveAux for every slot).
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	got := r.AuxModels()
	if got.Curator.Provider == "" || got.Keeper.Provider == "" {
		t.Errorf("AuxModels() with no WithAuxiliaryModels should fall back to defaults; got %+v", got)
	}
}

func TestRouter_WithAuxiliaryModels_RoundTrips(t *testing.T) {
	t.Parallel()
	custom := llm.AuxiliaryModels{
		Keeper: llm.AuxModel{Provider: "ollama", Model: "llama3", Timeout: 1 * time.Second},
	}
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithAuxiliaryModels(custom))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	got := r.AuxModels()
	if got.Keeper.Provider != "ollama" || got.Keeper.Model != "llama3" {
		t.Errorf("AuxModels() = %+v; want Keeper=ollama/llama3", got.Keeper)
	}
	// Other slots stay zero — caller deliberately wired a partial
	// config so the unconfigured rows surface in the status response.
	if got.Curator.Provider != "" {
		t.Errorf("AuxModels().Curator = %+v; want zero when only Keeper was set", got.Curator)
	}
}
