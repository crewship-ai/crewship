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
	return NewAuxStatusHandler(cfg, logger)
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

	// MVP defaults: 5 explicit slots, every one on anthropic/claude-haiku-4-5
	// (Fallback is NOT in the slot list — it's an internal backstop).
	if got, want := len(resp.Slots), 5; got != want {
		t.Fatalf("slot count = %d, want %d", got, want)
	}
	wantOrder := []string{"curator", "keeper", "behavior", "memory_health", "negative"}
	for i, row := range resp.Slots {
		if row.Slot != wantOrder[i] {
			t.Errorf("slots[%d].Slot = %q, want %q", i, row.Slot, wantOrder[i])
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

	for _, row := range resp.Slots {
		if row.Source != "fallback" {
			t.Errorf("slot %q source = %q, want fallback (only Fallback configured)", row.Slot, row.Source)
		}
		if row.TimeoutMS != 9000 {
			t.Errorf("slot %q TimeoutMS = %d, want 9000 (fallback timeout)", row.Slot, row.TimeoutMS)
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

	for _, row := range resp.Slots {
		switch row.Slot {
		case "keeper":
			if row.Source != "explicit" {
				t.Errorf("keeper source = %q, want explicit", row.Source)
			}
			if row.Provider != "ollama" || row.Model != "phi3:mini" {
				t.Errorf("keeper provider/model = %s/%s, want ollama/phi3:mini", row.Provider, row.Model)
			}
		default:
			if row.Source != "fallback" {
				t.Errorf("%s source = %q, want fallback", row.Slot, row.Source)
			}
			if row.Provider != "anthropic" {
				t.Errorf("%s provider = %q, want anthropic (fallback)", row.Slot, row.Provider)
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

	if len(resp.Slots) != 5 {
		t.Fatalf("slot count = %d, want 5", len(resp.Slots))
	}
	for _, row := range resp.Slots {
		if row.Source != "unconfigured" {
			t.Errorf("slot %q source = %q, want unconfigured", row.Slot, row.Source)
		}
		if row.Provider != "" || row.Model != "" {
			t.Errorf("slot %q should be blank when unconfigured; got %s/%s", row.Slot, row.Provider, row.Model)
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
