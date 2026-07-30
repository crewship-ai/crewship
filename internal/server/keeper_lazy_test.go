package server

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	_ "modernc.org/sqlite"
)

// The lazy gatekeeper is what turns `keeper.enabled` from a boot-time fact into
// a runtime one. Everything below is about the two ways that can go wrong:
// evaluating with a judge built from stale configuration, and rebuilding a
// judge for a change that did not affect its wiring.

const lazyTestDDL = `
CREATE TABLE users (id TEXT PRIMARY KEY);
CREATE TABLE keeper_runtime_settings (
    id                 TEXT PRIMARY KEY CHECK (id = 'singleton'),
    enabled            INTEGER CHECK (enabled IN (0, 1)),
    judge_provider     TEXT NOT NULL DEFAULT '',
    judge_endpoint_url TEXT NOT NULL DEFAULT '',
    judge_wire         TEXT NOT NULL DEFAULT '',
    judge_model        TEXT NOT NULL DEFAULT '',
    updated_by         TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);`

func newLazyTestStore(t *testing.T, dflt keepercfg.Defaults) *keepercfg.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(lazyTestDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	s := keepercfg.New(db, dflt)
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

// stubEvaluator records the model it was built for and how often it was asked.
type stubEvaluator struct {
	model string
	calls int
}

func (s *stubEvaluator) Evaluate(context.Context, gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	s.calls++
	return keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "stub " + s.model, RiskScore: 1}, nil
}

// recordingBuilder hands out a fresh stub per build so the test can count
// builds, which is the thing the fingerprint cache exists to keep down.
type recordingBuilder struct {
	built []*stubEvaluator
	err   error
}

func (b *recordingBuilder) build(eff keepercfg.Effective) (gatekeeper.Evaluator, error) {
	if b.err != nil {
		return nil, b.err
	}
	e := &stubEvaluator{model: eff.Model.Value}
	b.built = append(b.built, e)
	return e, nil
}

var enabledDefaults = keepercfg.Defaults{Enabled: true, EndpointURL: "http://127.0.0.1:11434", Model: "qwen2.5:7b"}

// Disabled Keeper must answer exactly as the old nil-gatekeeper wiring did:
// deny, "not configured", risk 10 — and never build a judge or dial anything.
func TestLazyGatekeeper_DisabledMatchesTheOldNilWiring(t *testing.T) {
	store := newLazyTestStore(t, keepercfg.Defaults{})
	b := &recordingBuilder{}
	gk := newLazyGatekeeper(store, nil, b.build)

	resp, err := gk.Evaluate(context.Background(), gatekeeper.EvalRequest{})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp.Decision != string(keeper.DecisionDeny) {
		t.Errorf("decision = %s, want DENY", resp.Decision)
	}
	if resp.Reason != keeperNotConfiguredReason {
		t.Errorf("reason = %q, want %q", resp.Reason, keeperNotConfiguredReason)
	}
	if resp.RiskScore != 10 {
		t.Errorf("risk = %d, want 10", resp.RiskScore)
	}
	if len(b.built) != 0 {
		t.Errorf("built %d judges while disabled", len(b.built))
	}
}

// The point of the whole slice: enabling Keeper through the store makes the
// next evaluation go to a real judge, with no restart in between.
func TestLazyGatekeeper_EnableTakesEffectWithoutRestart(t *testing.T) {
	store := newLazyTestStore(t, keepercfg.Defaults{})
	b := &recordingBuilder{}
	gk := newLazyGatekeeper(store, nil, b.build)
	ctx := context.Background()

	if resp, _ := gk.Evaluate(ctx, gatekeeper.EvalRequest{}); resp.Decision != string(keeper.DecisionDeny) {
		t.Fatalf("expected DENY before enabling, got %s", resp.Decision)
	}

	on := keepercfg.TriOn
	if _, err := store.Apply(ctx, keepercfg.Patch{
		Enabled:     &on,
		EndpointURL: ptr("http://127.0.0.1:11434"),
		Model:       ptr("qwen2.5:7b"),
	}, "operator"); err != nil {
		t.Fatalf("enable: %v", err)
	}

	resp, err := gk.Evaluate(ctx, gatekeeper.EvalRequest{})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp.Decision != string(keeper.DecisionAllow) {
		t.Errorf("decision = %s, want the stub judge's ALLOW", resp.Decision)
	}
	if len(b.built) != 1 {
		t.Fatalf("built %d judges, want 1", len(b.built))
	}
	if b.built[0].model != "qwen2.5:7b" {
		t.Errorf("judge built for model %q", b.built[0].model)
	}
}

// And the other direction, which is the one that matters operationally: turning
// Keeper off must stop routing credential decisions through a model.
func TestLazyGatekeeper_DisableTakesEffectWithoutRestart(t *testing.T) {
	store := newLazyTestStore(t, enabledDefaults)
	b := &recordingBuilder{}
	gk := newLazyGatekeeper(store, nil, b.build)
	ctx := context.Background()

	if resp, _ := gk.Evaluate(ctx, gatekeeper.EvalRequest{}); resp.Decision != string(keeper.DecisionAllow) {
		t.Fatalf("expected the stub judge to answer while enabled, got %s", resp.Decision)
	}
	off := keepercfg.TriOff
	if _, err := store.Apply(ctx, keepercfg.Patch{Enabled: &off}, "operator"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	resp, _ := gk.Evaluate(ctx, gatekeeper.EvalRequest{})
	if resp.Decision != string(keeper.DecisionDeny) || resp.Reason != keeperNotConfiguredReason {
		t.Errorf("after disable: %s / %q", resp.Decision, resp.Reason)
	}
}

// A model change must reach the judge; an edit that changes nothing about the
// wiring must not throw away a warm provider.
func TestLazyGatekeeper_RebuildsOnlyWhenTheWiringChanges(t *testing.T) {
	store := newLazyTestStore(t, enabledDefaults)
	b := &recordingBuilder{}
	gk := newLazyGatekeeper(store, nil, b.build)
	ctx := context.Background()

	for range 3 {
		if _, err := gk.Evaluate(ctx, gatekeeper.EvalRequest{}); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
	}
	if len(b.built) != 1 {
		t.Fatalf("built %d judges for three evaluations of one config, want 1", len(b.built))
	}

	if _, err := store.Apply(ctx, keepercfg.Patch{Model: ptr("qwen3:4b")}, "operator"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	resp, err := gk.Evaluate(ctx, gatekeeper.EvalRequest{})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(b.built) != 2 {
		t.Fatalf("built %d judges after a model change, want 2", len(b.built))
	}
	if resp.Reason != "stub qwen3:4b" {
		t.Errorf("evaluation went to %q, not the rebuilt judge", resp.Reason)
	}
	// The superseded judge must be dropped, not kept alongside.
	if b.built[0].calls != 3 {
		t.Errorf("old judge served %d calls, want the 3 from before the change", b.built[0].calls)
	}
}

// Keeper is fail-closed. A judge that cannot be built must surface as an error
// the handler turns into a DENY, never as a nil evaluator to dereference.
func TestLazyGatekeeper_BuildFailureIsAnError(t *testing.T) {
	store := newLazyTestStore(t, enabledDefaults)
	b := &recordingBuilder{err: errors.New("no provider for you")}
	gk := newLazyGatekeeper(store, nil, b.build)

	if _, err := gk.Evaluate(context.Background(), gatekeeper.EvalRequest{}); err == nil {
		t.Error("build failure did not surface as an error")
	}
}

// Enabled with no judge is refused at configure time, but KEEPER_ENABLED with
// no model can still arrive from the environment. Report the configuration
// problem instead of dialling an empty URL.
func TestLazyGatekeeper_EnabledWithoutAJudge(t *testing.T) {
	store := newLazyTestStore(t, keepercfg.Defaults{Enabled: true})
	b := &recordingBuilder{}
	gk := newLazyGatekeeper(store, nil, b.build)

	resp, err := gk.Evaluate(context.Background(), gatekeeper.EvalRequest{})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if resp.Decision != string(keeper.DecisionDeny) {
		t.Errorf("decision = %s, want DENY", resp.Decision)
	}
	if resp.Reason == keeperNotConfiguredReason {
		t.Error("reported Keeper as disabled when it is enabled but has no judge — the operator needs the difference")
	}
	if len(b.built) != 0 {
		t.Errorf("built %d judges with no endpoint or model", len(b.built))
	}
}

func ptr[T any](v T) *T { return &v }
