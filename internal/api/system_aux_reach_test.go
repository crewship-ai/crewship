package api

// "Configured" is not "answering". The judge-model card reported a healthy
// access judge on a box where Ollama was not running at all: the provider
// builds fine (NewOllama never dials), so a buildability check cannot tell
// the difference. That is the gap this probe closes.
//
// Only self-hosted providers are probed. A paid API is deliberately NOT
// dialled to render a status card — an admin refreshing the page must not
// cost money, and an unreachable Anthropic is a far rarer failure than a
// local model server that is simply down.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/llm"
)

func reachRow(t *testing.T, h *AuxStatusHandler, id string) auxSubsystemRow {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/aux-status?workspace_id=ws1", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	var out auxStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	row, found := rowByID(out.Subsystems, id)
	if !found {
		t.Fatalf("row %q missing from %s", id, rr.Body.String())
	}
	return row
}

func TestAuxReach_LocalJudgeAnswering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewAuxStatusHandler(llm.AuxiliaryModels{}, &config.KeeperConfig{
		Enabled: true, OllamaURL: srv.URL, Model: "qwen2.5:7b",
	}, newTestLogger())

	row := reachRow(t, h, "access_gatekeeper")
	if row.Reachable == nil || !*row.Reachable {
		t.Errorf("reachable = %v, want true — the model server answered", row.Reachable)
	}
}

func TestAuxReach_LocalJudgeNotRunning(t *testing.T) {
	// Closed immediately: nothing is listening, exactly like dev3.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	h := NewAuxStatusHandler(llm.AuxiliaryModels{}, &config.KeeperConfig{
		Enabled: true, OllamaURL: url, Model: "qwen2.5:7b",
	}, newTestLogger())

	row := reachRow(t, h, "access_gatekeeper")
	if row.Reachable == nil || *row.Reachable {
		t.Fatalf("reachable = %v, want false — nothing is listening", row.Reachable)
	}
	if row.ReachDetail == "" {
		t.Error("an unreachable judge must say so; that reason is the entire point")
	}
	// Buildability is unchanged — the two questions stay separate.
	if !row.Healthy {
		t.Error("healthy is about configuration and must not be clobbered by reachability")
	}
}

func TestAuxReach_PaidProviderIsNotDialled(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-not-used")
	cfg := llm.AuxiliaryModels{
		Curator:  llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
		Fallback: llm.AuxModel{Provider: "anthropic", Model: "claude-haiku-4-5"},
	}
	h := NewAuxStatusHandler(cfg, nil, newTestLogger())

	row := reachRow(t, h, "curator")
	// nil means "not probed" — an honest third state. Claiming either true or
	// false here would be a guess, and rendering a status card must never
	// spend money on an external API call.
	if row.Reachable != nil {
		t.Errorf("reachable = %v, want nil (not probed) for a paid provider", *row.Reachable)
	}
	if row.ReachDetail == "" {
		t.Error("the card has to explain why this one is not probed, or it reads as broken")
	}
}

func TestAuxReach_DisabledJudgeIsNotProbed(t *testing.T) {
	h := NewAuxStatusHandler(llm.AuxiliaryModels{}, &config.KeeperConfig{
		Enabled: false, OllamaURL: "http://127.0.0.1:1", Model: "qwen2.5:7b",
	}, newTestLogger())

	row := reachRow(t, h, "access_gatekeeper")
	// Dialling a judge that is switched off wastes the timeout and tells the
	// operator nothing they did not already know from `healthy`.
	if row.Reachable != nil {
		t.Errorf("reachable = %v, want nil for a disabled judge", *row.Reachable)
	}
}

func TestAuxReach_ProbeIsCached(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewAuxStatusHandler(llm.AuxiliaryModels{}, &config.KeeperConfig{
		Enabled: true, OllamaURL: srv.URL, Model: "qwen2.5:7b",
	}, newTestLogger())

	for i := 0; i < 5; i++ {
		reachRow(t, h, "access_gatekeeper")
	}
	// Several admins with the card open must not turn a status read into a
	// poll loop against the model server.
	if hits != 1 {
		t.Errorf("probed %d times for 5 status reads, want 1 (cached)", hits)
	}
}

func TestAuxReach_ProbeHasATimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // longer than the probe budget
	}))
	defer srv.Close()

	h := NewAuxStatusHandler(llm.AuxiliaryModels{}, &config.KeeperConfig{
		Enabled: true, OllamaURL: srv.URL, Model: "qwen2.5:7b",
	}, newTestLogger())

	start := time.Now()
	row := reachRow(t, h, "access_gatekeeper")
	// A hung model server must not hang the admin console.
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Errorf("status took %s — the probe timeout is not bounding it", elapsed)
	}
	if row.Reachable == nil || *row.Reachable {
		t.Error("a timed-out probe is not reachable")
	}
}
