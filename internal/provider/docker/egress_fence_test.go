package docker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestEgressFenceWanted(t *testing.T) {
	p := &Provider{cfg: Config{EgressFenceCrews: []string{"lab", "cm-id-2"}}}
	for _, tc := range []struct {
		team provider.CrewConfig
		want bool
	}{
		{provider.CrewConfig{ID: "x", Slug: "lab"}, true},
		{provider.CrewConfig{ID: "cm-id-2", Slug: "other"}, true},
		{provider.CrewConfig{ID: "y", Slug: "marketing"}, false},
		{provider.CrewConfig{}, false},
	} {
		if got := p.egressFenceWanted(tc.team); got != tc.want {
			t.Fatalf("wanted(%+v) = %v", tc.team, got)
		}
	}
	if (&Provider{}).egressFenceWanted(provider.CrewConfig{Slug: "lab"}) {
		t.Fatal("an empty pilot list must fence nothing")
	}
}

func TestEgressFenceApplicable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime string
		team    provider.CrewConfig
		ok      bool
	}{
		{"restricted runc", "runc", provider.CrewConfig{NetworkMode: "restricted"}, true},
		{"free has nothing to fence", "runc", provider.CrewConfig{NetworkMode: "free"}, false},
		{"privileged breaks the uid boundary", "runc", provider.CrewConfig{NetworkMode: "restricted", Privileged: true}, false},
		{"gvisor netstack bypasses the namespace", "runsc", provider.CrewConfig{NetworkMode: "restricted"}, false},
		{"declared services would be unreachable", "runc", provider.CrewConfig{NetworkMode: "restricted", Services: []provider.CrewService{{Name: "pg"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_RUNTIME", tc.runtime)
			err := (&Provider{}).egressFenceApplicable(tc.team)
			if tc.ok != (err == nil) {
				t.Fatalf("applicable err=%v, want ok=%v", err, tc.ok)
			}
			if err != nil && !errors.Is(err, errFenceUnsupported) {
				t.Fatalf("refusal must wrap errFenceUnsupported: %v", err)
			}
		})
	}
}

// Off by default: a crew not on the list never reaches the Docker client, so
// every existing crew keeps today's behaviour byte for byte.
func TestEnsureEgressFenceNoopWhenNotListed(t *testing.T) {
	p := &Provider{logger: slog.Default()} // nil client: any call would panic
	if err := p.ensureEgressFence(context.Background(), provider.CrewConfig{Slug: "lab", NetworkMode: "restricted"}, "cid", "img"); err != nil {
		t.Fatalf("unlisted crew: %v", err)
	}
}

// Listed but not applicable fails closed rather than running unfenced.
func TestEnsureEgressFenceRefusesInapplicable(t *testing.T) {
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	p := &Provider{cfg: Config{EgressFenceCrews: []string{"lab"}}, logger: slog.Default()}
	err := p.ensureEgressFence(context.Background(), provider.CrewConfig{Slug: "lab", NetworkMode: "restricted", Privileged: true}, "cid", "img")
	if !errors.Is(err, errFenceUnsupported) {
		t.Fatalf("privileged listed crew must be refused, got %v", err)
	}
}

// A caller that gives up must not take the crew down, but the helper's own
// timeout must (review of #2760: the decision is on the caller's context,
// not on the error class).
func TestStopUnfencedRespectsOnlyTheCallersContext(t *testing.T) {
	p := &Provider{logger: slog.Default()} // nil client: a stop would panic
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Cancelled caller: returns without touching the client.
	p.stopUnfenced(ctx, provider.CrewConfig{ID: "c"}, "cid", context.Canceled)
	defer func() {
		if recover() == nil {
			t.Fatal("a live caller with a fence failure must reach the stop")
		}
	}()
	// Live caller, helper deadline: must stop (reaches the nil client).
	p.stopUnfenced(context.Background(), provider.CrewConfig{ID: "c"}, "cid", context.DeadlineExceeded)
}

// A stop drops the confirmed-start record first; removing the container
// afterwards must still drop the crew mapping and the lock, or anyFenced
// stays true for the life of the process (review of #2760).
func TestForgetFencedAfterStop(t *testing.T) {
	p := &Provider{}
	id := "abc123def4567890"
	p.fenced.Store(id, "2026-10-02T12:00:00Z")
	p.fencedCrew.Store(id, "crew-1")
	p.fenceLocks.Store(id, &sync.Mutex{})
	p.fenced.Delete(id) // what stopCrewContainer does
	p.forgetFenced(id[:12])
	if p.anyFenced() {
		t.Fatal("crew mapping survived removal")
	}
	if _, ok := p.fenceLocks.Load(id); ok {
		t.Fatal("per-container lock survived removal")
	}
}
