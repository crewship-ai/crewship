package apple

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// syncBuffer is a bytes.Buffer safe for the provider's logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// newLoggingTestProvider is newTestProvider with a readable log sink.
func newLoggingTestProvider(cfg Config) (*Provider, *syncBuffer) {
	buf := &syncBuffer{}
	return &Provider{
		cfg:    withTestSidecarArtefacts(cfg),
		logger: slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		execs:  make(map[string]*execEntry),
		done:   make(chan struct{}),
	}, buf
}

// TestEnsureCrewRuntime_RestrictedEgressIsEnforcedAndSaysNothing: the mount
// that carries the proxy binary lands now (#1649), so a restricted crew is
// actually fenced and there is nothing to report. The warning this test used
// to assert would today be a false one, and a false one with teeth — the same
// report drives the agent's system prompt.
func TestEnsureCrewRuntime_RestrictedEgressIsEnforcedAndSaysNothing(t *testing.T) {
	installFakeContainer(t, crewBody)
	p, logs := newLoggingTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID: "crew1", Slug: "eng", NetworkMode: "restricted",
		AllowedDomains: []string{"api.example.com"},
	})
	if err != nil {
		t.Fatalf("a restricted crew must start: %v", err)
	}
	if id == "" {
		t.Fatal("want a container id")
	}
	if out := logs.String(); strings.Contains(out, "NetworkMode") {
		t.Errorf("egress is enforced here; naming it as a drop would be a false warning. log:\n%s", out)
	}
}

// TestEnsureCrewRuntime_UnfenceableEgressIsStillReported keeps #1648's
// assertion alive for the state that still produces it — a deployment with no
// sidecar binary — and keeps it on the path where it is reachable. Cold create
// now refuses outright rather than building a container with no proxy in it
// (the same refusal the docker provider has always made for the same
// misconfiguration), so the surviving case is warm reuse: the report runs
// ahead of the container lookup by design, the log names the drop, and the
// running container is still handed back rather than the crew being blocked.
func TestEnsureCrewRuntime_UnfenceableEgressIsStillReported(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  list) echo '[{"status":{"state":"running"},"configuration":{"id":"crewship-team-eng-crew1"}}]'; exit 0;;
esac
exit 0`)
	p, logs := newLoggingTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})
	p.cfg.SidecarBinaryPath = ""

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID: "crew1", Slug: "eng", NetworkMode: "restricted",
	})
	if err != nil {
		t.Fatalf("an unenforceable egress mode must not block a crew whose container is already up: %v", err)
	}
	if errors.Is(err, provider.ErrCrewConfigRefused) {
		t.Fatal("nothing is refused today")
	}
	if id != "crewship-team-eng-crew1" {
		t.Fatalf("id = %q, want the running container handed back", id)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "NetworkMode") {
		t.Errorf("the unenforced egress mode must be logged at WARN and named; log:\n%s", out)
	}
}

// TestEnsureCrewRuntime_NoConfigIsRefused walks the paths a refusal would have
// short-circuited — cold create and warm reuse — and pins that both still
// return a container for a crew asking for a mode this provider cannot apply.
func TestEnsureCrewRuntime_NoConfigIsRefused(t *testing.T) {
	t.Run("cold create", func(t *testing.T) {
		fake := installFakeContainer(t, crewBody)
		p := newTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})
		if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
			ID: "crew1", Slug: "eng", NetworkMode: "restricted",
			AllowedDomains: []string{"api.example.com"},
		}); err != nil {
			t.Fatalf("EnsureCrewRuntime: %v", err)
		}
		if !fake.hasCall(t, "create") {
			t.Errorf("the container must actually be created, calls: %v", fake.calls(t))
		}
	})

	t.Run("warm reuse", func(t *testing.T) {
		installFakeContainer(t, `
case "$1" in
  list) echo '[{"status":{"state":"running"},"configuration":{"id":"crewship-team-eng-crew1"}}]'; exit 0;;
esac
exit 0`)
		p := newTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})
		id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
			ID: "crew1", Slug: "eng", NetworkMode: "restricted",
		})
		if err != nil {
			t.Fatalf("EnsureCrewRuntime: %v", err)
		}
		if id != "crewship-team-eng-crew1" {
			t.Errorf("id = %q, want the running container handed back", id)
		}
	})
}

// TestEnsureCrewRuntime_DroppedCapabilitiesAreLoggedNotSilent is the other
// half of the issue: fields that cost a capability rather than a containment
// control still must not vanish. The crew starts, and the log names what it
// lost.
func TestEnsureCrewRuntime_DroppedCapabilitiesAreLoggedNotSilent(t *testing.T) {
	installFakeContainer(t, crewBody)
	p, logs := newLoggingTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID: "crew1", Slug: "eng",
		CachedImage:  "crewship-cache:abc123",
		ContainerEnv: map[string]string{"FOO": "bar"},
		LoginPath:    "/usr/local/bin:/usr/bin",
	})
	if err != nil {
		t.Fatalf("dropped capabilities must not block the crew: %v", err)
	}
	if id == "" {
		t.Fatal("want a container id")
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("dropped fields must be logged at WARN, not swallowed; log:\n%s", out)
	}
	// CachedImage is deliberately absent: the provider runs the crew's
	// provisioned image now, so reporting it as dropped would be false (#1779).
	// ContainerEnv is deliberately absent: the provider passes every entry as
	// --env now, so reporting it as dropped would be false (#1779).
	// TTLHours is deliberately absent too: idle auto-stop is the orchestrator's
	// reaper on every provider, and this one now feeds it (idle_ttl_test.go).
	for _, want := range []string{"LoginPath"} {
		if !strings.Contains(out, want) {
			t.Errorf("log must name the dropped field %q; log:\n%s", want, out)
		}
	}
}

// TestEnsureCrewRuntime_HonouredConfigLogsNoWarning guards against the
// mechanism becoming background noise: a crew whose whole request is honoured
// gets no capability warning at all.
func TestEnsureCrewRuntime_HonouredConfigLogsNoWarning(t *testing.T) {
	installFakeContainer(t, crewBody)
	p, logs := newLoggingTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})

	if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID: "crew1", Slug: "eng", MemoryMB: 2048, CPUs: 2, NetworkMode: "free",
	}); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a fully honoured config must produce no capability warning; log:\n%s", logs.String())
	}
}
