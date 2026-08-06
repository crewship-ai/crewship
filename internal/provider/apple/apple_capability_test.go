package apple

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

func supportFor(t *testing.T, cfg provider.CrewConfig) provider.CrewConfigSupport {
	t.Helper()
	p := newTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "crewship/runtime:base"})
	return p.UnsupportedCrewConfig(cfg)
}

// supportForUnfenced asks the same question of a deployment with no sidecar
// binary to mount — the only remaining state in which this provider cannot
// enforce a restricted egress mode (#1649). The egress assertions below were
// written when that was every deployment; they are kept and pointed here
// rather than deleted, because the reporting they pin still has to work for
// the case that survives.
func supportForUnfenced(t *testing.T, cfg provider.CrewConfig) provider.CrewConfigSupport {
	t.Helper()
	p := newTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "crewship/runtime:base"})
	p.cfg.SidecarBinaryPath = ""
	return p.UnsupportedCrewConfig(cfg)
}

func hasField(fields []provider.DroppedField, name string) *provider.DroppedField {
	for i := range fields {
		if fields[i].Field == name {
			return &fields[i]
		}
	}
	return nil
}

// TestUnsupportedCrewConfig_RestrictedEgressIsEnforcedOnceTheSidecarIsMounted
// is the half of #1648's judgement that #1649 changed. The proxy binary is
// bind-mounted into the crew container now, and nothing else in the chain is
// provider-specific — the orchestrator starts the proxy through the plain Exec
// interface and every exec carries the proxy environment. So the fence is
// real, and the report must stop saying otherwise.
//
// This is not cosmetic. The entry feeds the crew read paths AND the agent's
// own system prompt, so a stale "not enforced" instructs every agent on this
// provider to treat its egress as open when it is not.
func TestUnsupportedCrewConfig_RestrictedEgressIsEnforcedOnceTheSidecarIsMounted(t *testing.T) {
	s := supportFor(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", NetworkMode: "restricted"})

	if got := hasField(s.Degraded, "NetworkMode"); got != nil {
		t.Fatalf("egress is enforced once the sidecar binary is mounted; reporting it unenforced tells "+
			"every agent here to act unfenced when it is not. Got: %+v", got)
	}
	if _, ok := s.Drop("NetworkMode"); ok {
		t.Error("the read surfaces look this up by name; it must be absent, not merely empty")
	}
}

// TestUnsupportedCrewConfig_RestrictedEgressIsReportedNotRefused is the
// judgement at the centre of #1648, kept intact and pointed at the state that
// still produces it: a deployment with no sidecar binary configured. The crew
// still runs, because the product is meant to work on every platform it can.
// What must not happen is silence.
func TestUnsupportedCrewConfig_RestrictedEgressIsReportedNotRefused(t *testing.T) {
	s := supportForUnfenced(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", NetworkMode: "restricted"})

	if len(s.Refused) != 0 {
		t.Fatalf("a crew must not be blocked over an unenforceable egress mode; Refused = %+v", s.Refused)
	}
	got := hasField(s.Degraded, "NetworkMode")
	if got == nil {
		t.Fatalf("unenforceable egress must still be reported; Degraded = %+v", s.Degraded)
	}
	if got.Value != "restricted" {
		t.Errorf("Value = %q, want the requested mode %q", got.Value, "restricted")
	}
	// The detail is what every read surface quotes back to the operator, so it
	// has to say what is missing and how to fix it. The remedy changed with
	// #1649 — it is no longer "move the crew to docker" but "give this
	// provider the binary it mounts", so the fragments pinned here changed
	// with it.
	for _, want := range []string{"crewship-sidecar", "unrestricted", "CREWSHIP_SIDECAR_PATH"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("report detail must mention %q; got:\n%s", want, got.Detail)
		}
	}
}

// TestUnsupportedCrewConfig_NothingIsRefusedToday pins the deliberate empty
// case. The Refused class and its error machinery stay for a future provider
// that genuinely cannot proceed, but no configuration this provider is handed
// blocks a crew — and if someone adds one, that is a product decision that
// should have to delete this test rather than slip in.
func TestUnsupportedCrewConfig_NothingIsRefusedToday(t *testing.T) {
	full := provider.CrewConfig{
		ID: "crew-1", Slug: "ops",
		NetworkMode: "restricted", AllowedDomains: []string{"api.example.com"},
		TTLHours: 4, Image: "ghcr.io/acme/dev:1", CachedImage: "crewship-cache:abc",
		ContainerEnv: map[string]string{"FOO": "bar"}, LoginPath: "/usr/bin",
		Privileged: true, Init: true,
		CapAdd: []string{"SYS_PTRACE"}, SecurityOpt: []string{"seccomp=unconfined"},
		ExtraMounts:       []provider.CrewMount{{Source: "/a", Target: "/b"}},
		PostStartCommands: []string{"./x.sh"}, InitHookEnabled: true,
		Services: []provider.CrewService{{Name: "db", Image: "postgres:16"}},
	}
	s := supportFor(t, full)
	if len(s.Refused) != 0 {
		t.Fatalf("nothing is refused today; Refused = %+v", s.Refused)
	}
	if s.RefusedError(providerName) != nil {
		t.Error("a report with no refusals must not produce an error")
	}
	if len(s.Degraded) == 0 {
		t.Fatal("a config setting every droppable field must report something")
	}
}

// TestUnsupportedCrewConfig_AllowedDomainsRideOnTheEgressReport pins that the
// allowlist is not reported as a second, separate problem — it is meaningless
// without the mode that activates it, and two entries for one cause reads as
// two independent failures. It also keeps the one entry that the read surfaces
// look up by name from being ambiguous.
func TestUnsupportedCrewConfig_AllowedDomainsRideOnTheEgressReport(t *testing.T) {
	s := supportForUnfenced(t, provider.CrewConfig{
		ID: "crew-1", Slug: "ops",
		NetworkMode:    "restricted",
		AllowedDomains: []string{"api.example.com"},
	})
	if len(s.Degraded) != 1 {
		t.Fatalf("want exactly one entry for the egress pair, got %+v", s.Degraded)
	}
	if hasField(s.Degraded, "AllowedDomains") != nil {
		t.Errorf("AllowedDomains must not be reported separately; Degraded = %+v", s.Degraded)
	}
	if !strings.Contains(s.Degraded[0].Detail, "AllowedDomains") {
		t.Errorf("the egress report must account for the allowlist too; got:\n%s", s.Degraded[0].Detail)
	}
}

// TestUnsupportedCrewConfig_EgressDropIsFindableByFieldName is the contract
// the crew read paths depend on: they ask for the "NetworkMode" entry by name,
// so renaming the field in the report silently turns every crew back into
// "enforced".
func TestUnsupportedCrewConfig_EgressDropIsFindableByFieldName(t *testing.T) {
	s := supportForUnfenced(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", NetworkMode: "restricted"})
	drop, ok := s.Drop("NetworkMode")
	if !ok {
		t.Fatalf("Drop(\"NetworkMode\") found nothing in %+v", s)
	}
	if drop.Detail == "" {
		t.Error("the entry must carry a reason — it is what every surface quotes")
	}

	free := supportForUnfenced(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", NetworkMode: "free"})
	if _, ok := free.Drop("NetworkMode"); ok {
		t.Error("free egress is honoured, so it must not appear as a drop")
	}
}

// TestUnsupportedCrewConfig_FreeEgressIsHonouredSilently: "free" is what this
// provider actually delivers, so there is nothing to say. A report keyed on the
// provider rather than on the crew's values would flag every crew here.
func TestUnsupportedCrewConfig_FreeEgressIsHonouredSilently(t *testing.T) {
	s := supportFor(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", NetworkMode: "free"})
	if !s.Empty() {
		t.Fatalf("free egress on a minimal crew has nothing to report, got %+v", s)
	}
}

// TestUnsupportedCrewConfig_MinimalConfigReportsNothing covers the
// GetOrCreateContainer path (orchestrator_lifecycle.go passes only {ID, Slug}):
// four honoured fields, so the crew starts with no noise.
func TestUnsupportedCrewConfig_MinimalConfigReportsNothing(t *testing.T) {
	s := supportFor(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", MemoryMB: 2048, CPUs: 2})
	if !s.Empty() {
		t.Fatalf("ID/Slug/MemoryMB/CPUs are all honoured, got %+v", s)
	}
}

// TestUnsupportedCrewConfig_EveryDroppedFieldIsReported walks the whole
// CrewConfig surface one field at a time. Each case sets exactly one field and
// asserts it comes back named. This is the test that fails when someone adds a
// 21st field to CrewConfig and forgets that this provider ignores it.
func TestUnsupportedCrewConfig_EveryDroppedFieldIsReported(t *testing.T) {
	cases := []struct {
		field string
		mut   func(*provider.CrewConfig)
		// detail fragments the message has to carry to be actionable
		wants []string
	}{
		// TTLHours is deliberately not in this table: idle auto-stop is the
		// orchestrator's reaper on every provider, and this one now supplies
		// both provider-side facts it needs (FindCrewContainer and
		// ContainerStatus.Uptime) — see idle_ttl_test.go, which pins the
		// absence of the entry along with the facts that earned it.
		{"LoginPath", func(c *provider.CrewConfig) { c.LoginPath = "/usr/bin:/bin" }, []string{"PATH"}},
		{"PostStartCommands", func(c *provider.CrewConfig) { c.PostStartCommands = []string{"./start.sh"} }, []string{"post-start"}},
		{"InitHookEnabled", func(c *provider.CrewConfig) { c.InitHookEnabled = true }, []string{"/crew/init.sh"}},
		{"ProvisionSink", func(c *provider.CrewConfig) {
			c.ProvisionSink = func(devcontainer.ProvisionEvent) {}
		}, []string{"journal"}},
		{"Services", func(c *provider.CrewConfig) {
			c.Services = []provider.CrewService{{Name: "db", Image: "postgres:16"}}
		}, []string{"db"}},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			cfg := provider.CrewConfig{ID: "crew-1", Slug: "ops"}
			tc.mut(&cfg)
			s := supportFor(t, cfg)

			if len(s.Refused) != 0 {
				t.Fatalf("%s costs a capability, not a containment control — it must not refuse the crew; Refused = %+v",
					tc.field, s.Refused)
			}
			got := hasField(s.Degraded, tc.field)
			if got == nil {
				t.Fatalf("%s is dropped by this provider and must be reported; Degraded = %+v", tc.field, s.Degraded)
			}
			for _, want := range tc.wants {
				if !strings.Contains(got.Value+" "+got.Detail, want) {
					t.Errorf("%s report must mention %q; got value=%q detail=%q", tc.field, want, got.Value, got.Detail)
				}
			}
		})
	}
}

// TestUnsupportedCrewConfig_InitIsHonouredNotDropped replaces the Init row
// that used to sit in the table above. The container is created with --init
// unconditionally (#1649), the same way the docker provider sets
// HostConfig.Init unconditionally, so there is no longer a configuration of
// this field the provider fails to honour. Reporting it would be the same
// stale-entry problem as the egress one, just cheaper.
func TestUnsupportedCrewConfig_InitIsHonouredNotDropped(t *testing.T) {
	for _, init := range []bool{true, false} {
		s := supportFor(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", Init: init})
		if got := hasField(s.Degraded, "Init"); got != nil {
			t.Errorf("Init=%v: an init process is always created now; got %+v", init, got)
		}
	}
}

// TestUnsupportedCrewConfig_ImageMatchingTheProvidersOwnIsNotADrop: a caller
// that passes the very image this provider is configured to run loses nothing,
// and warning there would train operators to ignore the warning.
func TestUnsupportedCrewConfig_ImageMatchingTheProvidersOwnIsNotADrop(t *testing.T) {
	s := supportFor(t, provider.CrewConfig{ID: "crew-1", Slug: "ops", Image: "crewship/runtime:base"})
	if hasField(s.Degraded, "Image") != nil {
		t.Fatalf("an image identical to the provider's own is honoured; Degraded = %+v", s.Degraded)
	}
}

// TestProviderImplementsCrewConfigReporter is the compile-time wiring check:
// without it the optional interface is never asserted successfully at runtime
// and the whole report silently disappears.
func TestProviderImplementsCrewConfigReporter(t *testing.T) {
	var _ provider.CrewConfigReporter = (*Provider)(nil)
}
