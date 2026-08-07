package apple

// ContainerUser is now answered from the runtime rather than from a constant,
// which made it one `container inspect` per exec that leaves ExecConfig.User
// empty. These tests pin the cache that removes that round trip, and — the
// reason the cache needs pinning at all — the invalidation that stops it from
// answering for a container that no longer exists.
//
// Why a cache is worth the hazard here:
//
//   - The answer is immutable for a container's lifetime. It is
//     `configuration.initProcess.user`, fixed at create and never edited; the
//     only way it changes is a DIFFERENT container.
//   - The cost is recurring, not one-off. The empty-User callers are the
//     polling ones: the listening-port scanner execs EVERY crew container
//     every 15s (listening_port_scanner.go), and containerstate.Capture fires
//     four more probes per snapshot. Every one of those was paying an extra
//     inspect.
//   - The cost is a process, not a socket call. Docker's ContainerUser is an
//     API call to a daemon over a unix socket; here it is fork+exec of the
//     `container` CLI, which also has a 5-minute watchdog because it has been
//     observed to wedge. The docker provider not caching is not an argument
//     that this one should not.
//
// And why the hazard is bounded rather than dismissed. A stale entry means
// "same name, different container" — on this provider the container id IS the
// name (configuration.id, set by --name), so a recreated crew reuses the key
// exactly. Two things bound it:
//
//   - Every in-product path that produces a different container behind a name
//     goes through this provider, and each one drops the entry
//     (EnsureCrewRuntime, StopCrewRuntime, RemoveCrewRuntime). That is the
//     primary invalidation, and it is exact.
//   - The TTL is the backstop for the only case the first cannot see: someone
//     recreating a container with the CLI behind the provider's back.
//
// What a stale entry can and cannot do is worth stating, because it is what
// makes the residual acceptable: resolveExecUser re-runs
// IsPrivilegedExecUser on whatever it is handed, cached or not, so a stale
// answer can never admit root. The worst it can do is exec as the previous
// container's non-root uid, or refuse — both fail-safe directions.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// mutableInspect installs a fake `container` binary whose `inspect` answer the
// test can rewrite mid-test — the recreated-container case, which is the whole
// point of the invalidation. Returns the fake and a setter for the payload.
func mutableInspect(t *testing.T, initial string) (*fakeCLI, func(string)) {
	t.Helper()
	dir := t.TempDir()
	payload := filepath.Join(dir, "inspect.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(payload, []byte(body), 0o600); err != nil {
			t.Fatalf("write inspect payload: %v", err)
		}
	}
	write(initial)
	fake := installFakeContainer(t, `
case "$1" in
  inspect) cat '`+payload+`'; exit 0;;
esac
exit 0
`)
	return fake, write
}

func countCalls(t *testing.T, f *fakeCLI, prefix string) int {
	t.Helper()
	n := 0
	for _, c := range f.calls(t) {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// testClock is a hand-advanced clock so the TTL is testable without sleeping.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time      { return c.t }
func (c *testClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newClockedProvider(t *testing.T, clock *testClock) *Provider {
	t.Helper()
	p := newTestProvider(Config{})
	p.now = clock.now
	return p
}

// The round trip the cache exists to remove. Two resolves of the same
// container must cost one inspect, not two.
func TestContainerUser_RepeatedResolvesInspectOnce(t *testing.T) {
	fake, _ := mutableInspect(t, readFixture(t, fixtureCrewContainer))
	p := newTestProvider(Config{})

	for i := 0; i < 3; i++ {
		got, err := p.ContainerUser(context.Background(), "crew-a")
		if err != nil {
			t.Fatalf("ContainerUser #%d: %v", i, err)
		}
		if got != agentContainerUser {
			t.Fatalf("ContainerUser #%d = %q, want %q", i, got, agentContainerUser)
		}
	}
	if n := countCalls(t, fake, "inspect"); n != 1 {
		t.Errorf("3 resolves ran %d inspects, want 1 — the answer is fixed at container create, so the extra CLI processes buy nothing", n)
	}
}

// One container's answer must not be served for another. The scanner walks
// every crew container in turn, so a cache keyed on nothing (or on the
// provider) would hand every container the first one's user.
func TestContainerUser_CacheIsPerContainer(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  inspect)
    printf '[{"status":{"state":"running"},"configuration":{"id":"%s","initProcess":{"user":{"raw":{"userString":"uid-of-%s"}}}}}]' "$2" "$2"
    exit 0;;
esac
exit 0
`)
	p := newTestProvider(Config{})

	for _, id := range []string{"crew-a", "crew-b"} {
		got, err := p.ContainerUser(context.Background(), id)
		if err != nil {
			t.Fatalf("ContainerUser(%s): %v", id, err)
		}
		if want := "uid-of-" + id; got != want {
			t.Errorf("ContainerUser(%s) = %q, want %q — the cache served another container's answer", id, got, want)
		}
	}
}

// The backstop. An operator who deletes and recreates a container with the CLI
// does not go through this provider, so nothing invalidates on their behalf;
// the entry must age out on its own rather than be trusted forever.
func TestContainerUser_CachedAnswerIsBoundedInTime(t *testing.T) {
	fake, _ := mutableInspect(t, readFixture(t, fixtureCrewContainer))
	clock := &testClock{t: time.Now()}
	p := newClockedProvider(t, clock)

	if _, err := p.ContainerUser(context.Background(), "crew-a"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	clock.add(containerUserCacheTTL - time.Second)
	if _, err := p.ContainerUser(context.Background(), "crew-a"); err != nil {
		t.Fatalf("resolve inside the window: %v", err)
	}
	if n := countCalls(t, fake, "inspect"); n != 1 {
		t.Fatalf("inspects inside the TTL window = %d, want 1", n)
	}

	clock.add(2 * time.Second) // now past the TTL
	if _, err := p.ContainerUser(context.Background(), "crew-a"); err != nil {
		t.Fatalf("resolve after the window: %v", err)
	}
	if n := countCalls(t, fake, "inspect"); n != 2 {
		t.Errorf("inspects after the TTL expired = %d, want 2 — a cached run-as user must not be trusted indefinitely", n)
	}
}

// The security case, and the reason this cache is keyed and invalidated rather
// than just bounded. A container recreated under the SAME name is a different
// container with a possibly different user, and on this provider the name IS
// the cache key. Serving the old answer would resolve an exec against a
// container that no longer exists.
//
// Each subtest performs the lifecycle transition through the provider, which
// is how every in-product recreation happens.
func TestContainerUser_RecreatedContainerIsNotServedTheOldAnswer(t *testing.T) {
	transitions := []struct {
		name string
		do   func(t *testing.T, p *Provider, id string)
	}{
		{"RemoveCrewRuntime", func(t *testing.T, p *Provider, id string) {
			if err := p.RemoveCrewRuntime(context.Background(), id); err != nil {
				t.Fatalf("RemoveCrewRuntime: %v", err)
			}
		}},
		{"StopCrewRuntime", func(t *testing.T, p *Provider, id string) {
			if err := p.StopCrewRuntime(context.Background(), id); err != nil {
				t.Fatalf("StopCrewRuntime: %v", err)
			}
		}},
	}

	for _, tr := range transitions {
		t.Run(tr.name, func(t *testing.T) {
			fake, setInspect := mutableInspect(t, readFixture(t, fixtureCrewContainer))
			p := newTestProvider(Config{})

			got, err := p.ContainerUser(context.Background(), "crew-a")
			if err != nil {
				t.Fatalf("ContainerUser: %v", err)
			}
			if got != agentContainerUser {
				t.Fatalf("ContainerUser = %q, want %q", got, agentContainerUser)
			}

			// The container goes away and comes back as root under the same name.
			tr.do(t, p, "crew-a")
			setInspect(readFixture(t, fixtureImageDefaultRoot))

			got, err = p.ContainerUser(context.Background(), "crew-a")
			if err != nil {
				t.Fatalf("ContainerUser after %s: %v", tr.name, err)
			}
			if got != "0:0" {
				t.Errorf("ContainerUser after %s = %q, want 0:0 — the answer for a container that this provider tore down must not survive it", tr.name, got)
			}

			// And the guard that consumes it must fire, which is the whole
			// point: a stale non-root answer would have exec'd into a root
			// container.
			_, execErr := p.Exec(context.Background(), provider.ExecConfig{
				ContainerID: "crew-a",
				Cmd:         []string{"id"},
			})
			if execErr == nil || !strings.Contains(execErr.Error(), "no safe non-root user") {
				t.Errorf("exec with an empty user after %s: err = %v, want the fail-closed refusal", tr.name, execErr)
			}
			if fake.hasCall(t, "exec") {
				t.Errorf("refused exec still ran the command: %v", fake.calls(t))
			}
		})
	}
}

// EnsureCrewRuntime is the other half of a recreation: the container that
// comes back under the name is built here, so the entry from the previous one
// must not outlive the call.
func TestContainerUser_EnsureCrewRuntimeDropsThePreviousContainersAnswer(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "inspect.json")
	if err := os.WriteFile(payload, []byte(readFixture(t, fixtureCrewContainer)), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	installFakeContainer(t, `
case "$1" in
  network) if [ "$2" = "list" ]; then echo '[{"name":"mynet"}]'; fi; exit 0;;
  list) echo '[]'; exit 0;;
  image) if [ "$2" = "list" ]; then echo '[]'; fi; exit 0;;
  create) echo 'crewship-team-eng-crew1'; exit 0;;
  start) exit 0;;
  inspect) cat '`+payload+`'; exit 0;;
esac
exit 0
`)
	p := newTestProvider(Config{OutputBasePath: t.TempDir(), RuntimeImage: "img:1"})
	name := p.CrewContainerName("crew1", "eng")

	if _, err := p.ContainerUser(context.Background(), name); err != nil {
		t.Fatalf("prime the cache: %v", err)
	}

	// The name is recreated, this time from an image with no non-root user.
	if err := os.WriteFile(payload, []byte(readFixture(t, fixtureImageDefaultRoot)), 0o600); err != nil {
		t.Fatalf("rewrite payload: %v", err)
	}
	if _, err := p.EnsureCrewRuntime(context.Background(), provider.CrewConfig{ID: "crew1", Slug: "eng"}); err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	got, err := p.ContainerUser(context.Background(), name)
	if err != nil {
		t.Fatalf("ContainerUser: %v", err)
	}
	if got != "0:0" {
		t.Errorf("ContainerUser after the container was recreated = %q, want 0:0 — EnsureCrewRuntime built a new container behind this name", got)
	}
}

// An inspect that failed says nothing about the user, so there is nothing to
// remember. Caching the failure would turn one unreachable-runtime moment into
// a whole TTL window of refused execs.
func TestContainerUser_InspectFailureIsNotCached(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ok")
	payload := filepath.Join(dir, "inspect.json")
	if err := os.WriteFile(payload, []byte(readFixture(t, fixtureCrewContainer)), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	installFakeContainer(t, `
case "$1" in
  inspect)
    if [ -f '`+marker+`' ]; then cat '`+payload+`'; exit 0; fi
    echo 'runtime unavailable' >&2; exit 1;;
esac
exit 0
`)
	p := newTestProvider(Config{})

	if _, err := p.ContainerUser(context.Background(), "crew-a"); err == nil {
		t.Fatal("a failing inspect must be an error")
	}
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	got, err := p.ContainerUser(context.Background(), "crew-a")
	if err != nil {
		t.Fatalf("ContainerUser once the runtime answers: %v", err)
	}
	if got != agentContainerUser {
		t.Errorf("ContainerUser = %q, want %q — a failed inspect must not be remembered as an answer", got, agentContainerUser)
	}
}
