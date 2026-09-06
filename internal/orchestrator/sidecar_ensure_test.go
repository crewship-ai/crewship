package orchestrator

// EnsureCrewSidecar is the crew-level door onto settleSidecar — the entry point
// a routine `script` step uses, because it has a crew and no agent. These tests
// pin the three things that make it safe to have a second door at all:
//
//   - it brings a sidecar up on a container no agent run has ever warmed
//     (the bug: script steps carry SidecarProxyEnv and had nothing to talk to);
//   - it boots that sidecar with the CREW's real egress policy, never an empty
//     or defaulted one, which would trade a loud failure for an open fence;
//   - it REUSES a healthy sidecar rather than restarting it, including one an
//     agent run started with a fuller configuration — and conversely, the agent
//     path replaces the thin crew-only sidecar instead of inheriting it.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ensureCrewOrch builds a bare orchestrator over a scripted container whose
// /health probe answers with `health` ("" = nothing listening on 9119).
func ensureCrewOrch(health string) (*Orchestrator, *covContainer) {
	c := &covContainer{route: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		if strings.Contains(covScript(cfg), "127.0.0.1:9119/health") {
			return covResult("health", health), nil
		}
		return nil, nil
	}}
	return &Orchestrator{container: c, logger: covQuietLogger(), sidecarEnabled: true}, c
}

func sidecarWasLaunched(scripts []string) (string, bool) {
	for _, s := range scripts {
		if covSidecarInputRE.MatchString(s) {
			return s, true
		}
	}
	return "", false
}

func TestEnsureCrewSidecar(t *testing.T) {
	freeHealth := `{"status":"ok","network_mode":"free"}`
	restrictedHealth := `{"status":"ok","network_mode":"restricted","domains_hash":"` +
		DomainsHash([]string{"api.github.com"}) + `"}`

	tests := []struct {
		name string
		spec CrewSidecarSpec
		// health is the running sidecar's /health body; "" is a cold container.
		health string
		// sidecarEnabled mirrors CREWSHIP_SIDECAR_ENABLED.
		sidecarEnabled bool
		wantErr        string
		wantStart      bool
		wantMode       string
		wantDomains    []string
	}{
		{
			name:           "cold crew, free mode — starts",
			spec:           CrewSidecarSpec{CrewID: "crew_1", ContainerID: "ctr_1", NetworkMode: "free"},
			sidecarEnabled: true,
			wantStart:      true,
			wantMode:       "free",
		},
		{
			name:           "empty network mode defaults to free, exactly like the agent path",
			spec:           CrewSidecarSpec{CrewID: "crew_1", ContainerID: "ctr_1"},
			sidecarEnabled: true,
			wantStart:      true,
			wantMode:       "free",
		},
		{
			name: "cold crew, restricted mode — the crew's allowlist reaches the sidecar",
			spec: CrewSidecarSpec{
				CrewID: "crew_1", ContainerID: "ctr_1",
				NetworkMode: "restricted", AllowedDomains: []string{"api.github.com"},
			},
			sidecarEnabled: true,
			wantStart:      true,
			wantMode:       "restricted",
			wantDomains:    []string{"api.github.com"},
		},
		{
			name:           "healthy free-mode sidecar is reused",
			spec:           CrewSidecarSpec{CrewID: "crew_1", ContainerID: "ctr_1", NetworkMode: "free"},
			health:         freeHealth,
			sidecarEnabled: true,
			wantStart:      false,
		},
		{
			name: "healthy restricted sidecar on the same allowlist is reused",
			spec: CrewSidecarSpec{
				CrewID: "crew_1", ContainerID: "ctr_1",
				NetworkMode: "restricted", AllowedDomains: []string{"api.github.com"},
			},
			health:         restrictedHealth,
			sidecarEnabled: true,
			wantStart:      false,
		},
		{
			name: "a sidecar still on the old policy is replaced",
			spec: CrewSidecarSpec{
				CrewID: "crew_1", ContainerID: "ctr_1",
				NetworkMode: "restricted", AllowedDomains: []string{"api.github.com"},
			},
			health:         freeHealth,
			sidecarEnabled: true,
			wantStart:      true,
			wantMode:       "restricted",
			wantDomains:    []string{"api.github.com"},
		},
		{
			name: "an unknown network mode is refused, never silently treated as free",
			spec: CrewSidecarSpec{
				CrewID: "crew_1", ContainerID: "ctr_1", NetworkMode: "airgapped",
			},
			sidecarEnabled: true,
			wantErr:        "unknown network mode",
		},
		{
			name:           "no container id is an error, not a silent skip",
			spec:           CrewSidecarSpec{CrewID: "crew_1", NetworkMode: "free"},
			sidecarEnabled: true,
			wantErr:        "no container id",
		},
		{
			name:           "sidecar disabled instance-wide is a no-op, not a failed step",
			spec:           CrewSidecarSpec{CrewID: "crew_1", ContainerID: "ctr_1", NetworkMode: "free"},
			sidecarEnabled: false,
			wantStart:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, c := ensureCrewOrch(tc.health)
			o.sidecarEnabled = tc.sidecarEnabled

			err := o.EnsureCrewSidecar(context.Background(), tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("EnsureCrewSidecar error = %v, want one containing %q", err, tc.wantErr)
				}
				if _, launched := sidecarWasLaunched(c.snapshotScripts()); launched {
					t.Error("a sidecar was started despite the refusal — the fence would be unknown")
				}
				return
			}
			if err != nil {
				t.Fatalf("EnsureCrewSidecar: %v", err)
			}

			script, launched := sidecarWasLaunched(c.snapshotScripts())
			if launched != tc.wantStart {
				if tc.wantStart {
					t.Fatalf("no sidecar started; a script step's HTTP_PROXY would point at nothing")
				}
				t.Fatalf("a healthy sidecar was restarted; reuse is what checkSidecar exists for")
			}
			if !tc.wantStart {
				return
			}

			payload := covDecodeSidecarInput(t, script)
			policy, _ := payload["network_policy"].(map[string]any)
			if policy == nil {
				t.Fatal("sidecar booted with NO network policy — the crew egress fence would not exist")
			}
			if got, _ := policy["mode"].(string); got != tc.wantMode {
				t.Errorf("network policy mode = %q, want %q", got, tc.wantMode)
			}
			var gotDomains []string
			for _, d := range asAnySlice(policy["allowed_domains"]) {
				s, _ := d.(string)
				gotDomains = append(gotDomains, s)
			}
			if strings.Join(gotDomains, ",") != strings.Join(tc.wantDomains, ",") {
				t.Errorf("allowed_domains = %v, want %v", gotDomains, tc.wantDomains)
			}
			// A crew-level start has no agent, so it has no credentials to
			// resolve. It must say so on the wire rather than look like an
			// ordinary configured sidecar — see crewOnlySidecarFingerprint.
			if creds := asAnySlice(payload["credentials"]); len(creds) != 0 {
				t.Errorf("crew-level start carried %d credentials; there is no crew-wide grant to resolve", len(creds))
			}
			if fp, _ := payload["config_fingerprint"].(string); fp != crewOnlySidecarFingerprint {
				t.Errorf("config_fingerprint = %q, want %q — without the marker the first agent run "+
					"inherits a credential-less sidecar", fp, crewOnlySidecarFingerprint)
			}
		})
	}
}

func asAnySlice(v any) []any {
	out, _ := v.([]any)
	return out
}

// The other half of the crew-only marker: an AGENT run must never inherit the
// thin sidecar a script step started. The ordinary config-fingerprint
// comparison cannot decide this on an instance with no internal auth — both
// sides' HMACs are empty, which sidecarNeedsRestart reads as "no opinion" — so
// the marker is compared on its own.
func TestCrewOnlySidecarMustBeReplaced(t *testing.T) {
	tests := []struct {
		name           string
		health         *sidecarHealth
		crewOnlyCaller bool
		want           bool
	}{
		{
			name:   "agent run finds a crew-started sidecar",
			health: &sidecarHealth{ConfigFingerprint: crewOnlySidecarFingerprint},
			want:   true,
		},
		{
			name:           "script step finds the sidecar it (or a peer step) started",
			health:         &sidecarHealth{ConfigFingerprint: crewOnlySidecarFingerprint},
			crewOnlyCaller: true,
			want:           false,
		},
		{
			name:   "agent run finds a normally-configured sidecar",
			health: &sidecarHealth{ConfigFingerprint: "a1b2c3d4e5f6a1b2c3d4e5f6"},
			want:   false,
		},
		{
			name:   "agent run finds a pre-fingerprint sidecar",
			health: &sidecarHealth{},
			want:   false,
		},
		{
			name:           "script step finds an agent-configured sidecar and keeps it",
			health:         &sidecarHealth{ConfigFingerprint: "a1b2c3d4e5f6a1b2c3d4e5f6"},
			crewOnlyCaller: true,
			want:           false,
		},
		{name: "nothing running", health: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := crewOnlySidecarMustBeReplaced(tc.health, tc.crewOnlyCaller); got != tc.want {
				t.Errorf("crewOnlySidecarMustBeReplaced = %v, want %v", got, tc.want)
			}
		})
	}
}

// End-to-end through settleSidecar, with internal auth UNCONFIGURED so both
// fingerprints are empty and only the marker separates the two callers. This is
// the case a plain fingerprint comparison silently gets wrong: the agent's model
// traffic would go out through a proxy holding none of its credentials.
func TestSettleSidecar_AgentReplacesACrewStartedSidecar(t *testing.T) {
	o, c := ensureCrewOrch(`{"status":"ok","network_mode":"free","config_fingerprint":"` +
		crewOnlySidecarFingerprint + `"}`)

	started, err := o.settleSidecar(context.Background(), sidecarSettleSpec{
		containerID:   "ctr_1",
		logID:         "agent_1",
		desiredMode:   "free",
		networkPolicy: &SidecarNetworkPolicy{Mode: "free"},
		// Internal auth unconfigured → sidecarConfigFingerprint returns "".
		configFingerprint:  "",
		restartFingerprint: "",
		creds:              []Credential{{EnvVarName: "ANTHROPIC_API_KEY", Type: "ANTHROPIC_API_KEY", PlainValue: "sk-ant-x"}},
	})
	if err != nil {
		t.Fatalf("settleSidecar: %v", err)
	}
	if !started {
		t.Fatal("the agent run reused a sidecar started with no credentials — its model calls " +
			"would go through a proxy that injects nothing")
	}
	scripts := c.snapshotScripts()
	pkilled := false
	for _, s := range scripts {
		if strings.Contains(s, "pkill -f '^crewship-sidecar'") {
			pkilled = true
		}
	}
	if !pkilled {
		t.Error("the crew-only sidecar was not killed before the replacement launched")
	}
}

// #1220's guarantee, at the crew door: concurrent script steps against one
// container must produce exactly one sidecar. Two starts means one step's proxy
// was killed out from under it mid-run.
func TestEnsureCrewSidecar_ConcurrentCallersStartOne(t *testing.T) {
	var mu sync.Mutex
	started := 0
	c := &covContainer{}
	c.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		if strings.Contains(covScript(cfg), "127.0.0.1:9119/health") {
			mu.Lock()
			body := ""
			if started > 0 {
				body = `{"status":"ok","network_mode":"free","config_fingerprint":"` +
					crewOnlySidecarFingerprint + `"}`
			}
			mu.Unlock()
			return covResult("health", body), nil
		}
		if strings.Contains(covStdin(cfg), "crewship-sidecar --addr") {
			mu.Lock()
			started++
			mu.Unlock()
			return covResult("sidecar-start", ""), nil
		}
		return nil, nil
	}
	o := &Orchestrator{container: c, logger: covQuietLogger(), sidecarEnabled: true}

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = o.EnsureCrewSidecar(context.Background(), CrewSidecarSpec{
				CrewID: "crew_1", ContainerID: "ctr_1", NetworkMode: "free",
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if started != 1 {
		t.Errorf("%d concurrent EnsureCrewSidecar calls started %d sidecars, want 1", len(errs), started)
	}
}
