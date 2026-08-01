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
		cfg:    cfg,
		logger: slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		execs:  make(map[string]*execEntry),
		done:   make(chan struct{}),
	}, buf
}

// TestEnsureCrewRuntime_RestrictedEgressStartsTheCrewAndSaysItIsUnenforced:
// the crew is not blocked over a setting this provider cannot apply — the
// product runs everywhere it can — but the daemon log names the drop, so the
// operator debugging "why did this reach the internet" finds it at the moment
// the container came up.
func TestEnsureCrewRuntime_RestrictedEgressStartsTheCrewAndSaysItIsUnenforced(t *testing.T) {
	installFakeContainer(t, crewBody)
	p, logs := newLoggingTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})

	id, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{
		ID: "crew1", Slug: "eng", NetworkMode: "restricted",
	})
	if err != nil {
		t.Fatalf("an unenforceable egress mode must not block the crew: %v", err)
	}
	if errors.Is(err, provider.ErrCrewConfigRefused) {
		t.Fatal("nothing is refused today")
	}
	if id == "" {
		t.Fatal("want a container id")
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
  list) echo '[{"status":"running","configuration":{"id":"crewship-team-eng-crew1"}}]'; exit 0;;
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
		TTLHours:     4,
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
	for _, want := range []string{"CachedImage", "ContainerEnv", "TTLHours"} {
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
