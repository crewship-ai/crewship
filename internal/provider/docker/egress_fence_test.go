package docker

import (
	"context"
	"errors"
	"log/slog"
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
