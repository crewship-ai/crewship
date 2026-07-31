package server

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
	_ "modernc.org/sqlite"
)

// The property this file exists for: an operator changes an evaluator's model in
// the console and the NEXT evaluation uses it, without a restart. The evaluators
// are built once at boot and their pointers are captured by the route handler, so
// the only way that can be true is through the per-request gov-model seam.

// auxStoreFor builds a store on a throwaway DB. The DDL mirrors
// internal/database/migrations/20260730111147_keeper_aux_settings.sql plus
// 20260730205811_keeper_aux_credential.sql.
func auxStoreFor(t *testing.T) *keepercfg.AuxStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// One connection: a bare :memory: DSN gives each pooled connection its own
	// empty database.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE users (id TEXT PRIMARY KEY);
		CREATE TABLE keeper_aux_settings (
			slot TEXT PRIMARY KEY, provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '', timeout_ms INTEGER,
			updated_by TEXT, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '',
			credential_id TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	s := keepercfg.NewAuxStore(db, llm.DefaultAuxiliaryModels())
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

func judgeAt(url, model string) func() (string, string) {
	return func() (string, string) { return url, model }
}

// No override ⇒ (nil, ""), which the gatekeeper reads as "use what you were
// built with". An instance nobody has configured must behave exactly as before.
func TestAuxLiveResolver_NoOverrideFallsThrough(t *testing.T) {
	store := auxStoreFor(t)
	resolve := newAuxLiveResolver("behavior", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), nil, nil, nil, slog.Default())

	if p, m := resolve(context.Background(), "ws-1"); p != nil || m != "" {
		t.Errorf("got (%v, %q), want a fall-through to the construction default", p, m)
	}
}

// The point of the seam: an override written after boot is in force on the next
// call, with no rebuild of the evaluator.
func TestAuxLiveResolver_OverrideAppliesWithoutRebuild(t *testing.T) {
	store := auxStoreFor(t)
	resolve := newAuxLiveResolver("behavior", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), nil, nil, nil, slog.Default())

	// Resolver already exists (as it does in production — built at boot).
	if p, _ := resolve(context.Background(), "ws-1"); p != nil {
		t.Fatal("resolved a provider before anything was overridden")
	}

	provider, model := "ollama", "llama3.1:8b"
	if _, err := store.Apply(context.Background(), "behavior",
		keepercfg.AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	p, m := resolve(context.Background(), "ws-1")
	if p == nil {
		t.Fatal("the override did not reach the evaluator")
	}
	if m != "llama3.1:8b" {
		t.Errorf("model = %q, want llama3.1:8b", m)
	}

	// The provider is cached on the wiring, so an unchanged config reuses it —
	// otherwise every sampled tool call would open a fresh connection and pay for
	// a cold model load.
	p2, _ := resolve(context.Background(), "ws-1")
	if p2 != p {
		t.Error("an unchanged override rebuilt the provider")
	}

	// And a changed model yields a different one.
	next := "qwen2.5:14b"
	if _, err := store.Apply(context.Background(), "behavior",
		keepercfg.AuxPatch{Model: &next}, ""); err != nil {
		t.Fatalf("apply second: %v", err)
	}
	p3, m3 := resolve(context.Background(), "ws-1")
	if p3 == p || m3 != "qwen2.5:14b" {
		t.Errorf("a changed override did not rebuild: model=%q same=%v", m3, p3 == p)
	}
}

// An "ollama" slot must dial the endpoint the instance is configured with, not
// the URL this process happened to boot with — the judge endpoint is settable at
// runtime, so a stale URL would silently send evaluations to a host the operator
// has moved away from. A missing endpoint is a fall-through, not a broken dial.
func TestAuxLiveResolver_LocalSlotNeedsAJudgeEndpoint(t *testing.T) {
	store := auxStoreFor(t)
	provider, model := "ollama", "llama3.1:8b"
	if _, err := store.Apply(context.Background(), "negative",
		keepercfg.AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	resolve := newAuxLiveResolver("negative", store, nil,
		judgeAt("", ""), nil, nil, nil, slog.Default())
	if p, _ := resolve(context.Background(), "ws-1"); p != nil {
		t.Error("built a local evaluator with no judge endpoint configured")
	}

	resolve = newAuxLiveResolver("negative", store, nil,
		judgeAt("http://10.0.0.5:11434", "qwen2.5:7b"), nil, nil, nil, slog.Default())
	if p, _ := resolve(context.Background(), "ws-1"); p == nil {
		t.Error("a configured judge endpoint did not produce a provider")
	}
}

// The per-workspace governance model is the more specific setting, so an
// instance-wide evaluator override must not silently undo a workspace that
// deliberately pinned its own.
func TestAuxLiveResolver_WorkspaceGovModelStillWins(t *testing.T) {
	store := auxStoreFor(t)
	provider, model := "ollama", "llama3.1:8b"
	if _, err := store.Apply(context.Background(), "curator",
		keepercfg.AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	ws := llm.NewOllama("http://127.0.0.1:11434", "workspace-pinned")
	next := gatekeeper.GovModelResolver(func(context.Context, string) (llm.Provider, string) {
		return ws, "workspace-pinned"
	})
	resolve := newAuxLiveResolver("curator", store, next,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), nil, nil, nil, slog.Default())

	p, m := resolve(context.Background(), "ws-1")
	if m != "workspace-pinned" || p != llm.Provider(ws) {
		t.Errorf("got %q, want the workspace's own governance model to win", m)
	}
}

// An unbuildable override (anthropic with no key) leaves the slot on its
// construction-time provider rather than taking the evaluator down.
func TestAuxLiveResolver_UnbuildableOverrideFallsThrough(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := auxStoreFor(t)
	provider, model := "anthropic", "claude-opus-5"
	if _, err := store.Apply(context.Background(), "memory_health",
		keepercfg.AuxPatch{Provider: &provider, Model: &model}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	resolve := newAuxLiveResolver("memory_health", store, nil,
		judgeAt("http://127.0.0.1:11434", "qwen2.5:7b"), nil, nil, nil, slog.Default())
	if p, _ := resolve(context.Background(), "ws-1"); p != nil {
		t.Error("built an anthropic provider with no API key")
	}
	// Repeated calls must stay quiet about it — the behaviour monitor samples tool
	// calls, so one missing key could otherwise write a log line per call.
	for range 3 {
		if p, _ := resolve(context.Background(), "ws-1"); p != nil {
			t.Fatal("provider appeared without a key")
		}
	}
}

// No store (test and embedded wirings) means the wrapper is not in the path at
// all, so those builds keep their exact previous behaviour.
func TestAuxLiveResolver_NilStoreReturnsTheOriginalResolver(t *testing.T) {
	called := false
	next := gatekeeper.GovModelResolver(func(context.Context, string) (llm.Provider, string) {
		called = true
		return nil, ""
	})
	resolve := newAuxLiveResolver("behavior", nil, next, nil, nil, nil, nil, slog.Default())
	resolve(context.Background(), "ws-1")
	if !called {
		t.Error("the original resolver was not called")
	}
	if resolve := newAuxLiveResolver("behavior", nil, nil, nil, nil, nil, nil, slog.Default()); resolve != nil {
		t.Error("a nil store with no next resolver should stay nil, not wrap")
	}
}
