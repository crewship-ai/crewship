package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// countingProvider models the container provider's per-crew idempotency: it
// cold-"starts" a container at most once per crew id no matter how many
// concurrent EnsureCrewRuntime calls arrive — exactly the contract #836 relies
// on so concurrent claims for one crew collapse to a single start. Embeds the
// full-interface fake and overrides only EnsureCrewRuntime.
type countingProvider struct {
	*orchCovContainer
	mu      sync.Mutex
	started map[string]string
	starts  int // cold starts (one per distinct crew)
	calls   int // total EnsureCrewRuntime calls
	lastCfg provider.CrewConfig
}

func newCountingProvider() *countingProvider {
	return &countingProvider{orchCovContainer: &orchCovContainer{}, started: map[string]string{}}
}

func (p *countingProvider) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastCfg = cfg
	if id, ok := p.started[cfg.ID]; ok {
		return id, nil // warm hit — no new start
	}
	p.starts++
	id := "ctr_" + cfg.ID
	p.started[cfg.ID] = id
	return id, nil
}

func TestPrewarmCrew_ResolvesConfigAndEnsures(t *testing.T) {
	cp := newCountingProvider()
	var gotCrew, gotWS string
	r := &OrchestratorRunner{
		container: cp,
		logger:    slog.Default(),
		crewRuntime: func(_ context.Context, crewID, ws string) (provider.CrewConfig, error) {
			gotCrew, gotWS = crewID, ws
			return provider.CrewConfig{ID: crewID, Slug: "growth", Image: "img:1"}, nil
		},
	}

	if err := r.PrewarmCrew(context.Background(), "crew_x", "ws_1"); err != nil {
		t.Fatalf("PrewarmCrew: %v", err)
	}
	if gotCrew != "crew_x" || gotWS != "ws_1" {
		t.Errorf("crewRuntime called with (%q,%q), want (crew_x,ws_1)", gotCrew, gotWS)
	}
	if cp.calls != 1 || cp.starts != 1 {
		t.Errorf("want 1 call / 1 start, got %d/%d", cp.calls, cp.starts)
	}
	if cp.lastCfg.Image != "img:1" || cp.lastCfg.Slug != "growth" {
		t.Errorf("EnsureCrewRuntime got minimal config, want resolved: %+v", cp.lastCfg)
	}
}

// The headline #836 guarantee: many concurrent claims for ONE crew must
// produce exactly one container start.
func TestPrewarmCrew_ConcurrentClaimsOneStart(t *testing.T) {
	cp := newCountingProvider()
	r := &OrchestratorRunner{
		container: cp,
		logger:    slog.Default(),
		crewRuntime: func(_ context.Context, crewID, _ string) (provider.CrewConfig, error) {
			return provider.CrewConfig{ID: crewID}, nil
		},
	}

	const n = 12
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = r.PrewarmCrew(context.Background(), "crew_shared", "ws_1")
		}()
	}
	wg.Wait()

	if cp.calls != n {
		t.Errorf("expected all %d prewarms to reach EnsureCrewRuntime, got %d", n, cp.calls)
	}
	if cp.starts != 1 {
		t.Fatalf("concurrent claims for one crew must start the container once, got %d", cp.starts)
	}
}

func TestPrewarmCrew_CrewRuntimeErrorFallsBackToMinimal(t *testing.T) {
	cp := newCountingProvider()
	r := &OrchestratorRunner{
		container: cp,
		logger:    slog.Default(),
		crewRuntime: func(_ context.Context, _, _ string) (provider.CrewConfig, error) {
			return provider.CrewConfig{}, errors.New("resolve boom")
		},
	}

	if err := r.PrewarmCrew(context.Background(), "crew_y", "ws_1"); err != nil {
		t.Fatalf("PrewarmCrew should swallow crewRuntime error and ensure with minimal config: %v", err)
	}
	if cp.starts != 1 || cp.lastCfg.ID != "crew_y" {
		t.Errorf("want minimal {ID:crew_y} ensure, got starts=%d cfg=%+v", cp.starts, cp.lastCfg)
	}
}

func TestPrewarmCrew_NilContainerOrEmptyCrewNoop(t *testing.T) {
	// Nil provider → no-op, no panic.
	r := &OrchestratorRunner{logger: slog.Default()}
	if err := r.PrewarmCrew(context.Background(), "crew_z", "ws"); err != nil {
		t.Errorf("nil container must no-op, got %v", err)
	}
	// Empty crew id → no-op even with a provider.
	cp := newCountingProvider()
	r2 := &OrchestratorRunner{container: cp, logger: slog.Default()}
	if err := r2.PrewarmCrew(context.Background(), "", "ws"); err != nil {
		t.Errorf("empty crew id must no-op, got %v", err)
	}
	if cp.calls != 0 {
		t.Errorf("empty crew id must not call EnsureCrewRuntime, got %d", cp.calls)
	}
}
