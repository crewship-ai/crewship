package docker

import (
	"bufio"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/network"
)

func TestCrewNetworkFor(t *testing.T) {
	p := &Provider{cfg: Config{Network: "crewship-2-agents", CrewNetworkCrews: []string{"ops", "cm-mkt"}}}
	for _, tc := range []struct{ id, slug, want string }{
		{"cm-ops", "ops", "crewship-2-agents-crew-cm-ops"},
		{"cm-mkt", "marketing", "crewship-2-agents-crew-cm-mkt"},
		{"cm-x", "x", "crewship-2-agents"},
	} {
		if got := p.crewNetworkFor(tc.id, tc.slug); got != tc.want {
			t.Fatalf("crewNetworkFor(%s,%s) = %q, want %q", tc.id, tc.slug, got, tc.want)
		}
	}
	if got := (&Provider{cfg: Config{Network: "n"}}).crewNetworkFor("a", "b"); got != "n" {
		t.Fatalf("empty list must keep the instance network, got %q", got)
	}
}

func TestCandidateSubnets(t *testing.T) {
	got := candidateSubnets(netip.MustParsePrefix("10.231.0.0/25"), 27)
	want := []string{"10.231.0.0/27", "10.231.0.32/27", "10.231.0.64/27", "10.231.0.96/27"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("subnet %d = %s, want %s", i, got[i], want[i])
		}
	}
	if candidateSubnets(netip.MustParsePrefix("10.0.0.0/28"), 27) != nil {
		t.Fatal("a pool smaller than one subnet yields nothing")
	}
}

func TestOverlapsAny(t *testing.T) {
	used := []netip.Prefix{netip.MustParsePrefix("10.231.0.32/27"), netip.MustParsePrefix("172.20.0.0/16")}
	if !overlapsAny(netip.MustParsePrefix("10.231.0.32/27"), used) || overlapsAny(netip.MustParsePrefix("10.231.0.0/27"), used) {
		t.Fatal("overlap detection wrong")
	}
}

func TestCrewNetworkPool(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{"", true}, {"10.231.0.0/16", true}, {"10.231.0.0/27", true},
		{"10.231.0.0/28", false}, {"fd00::/64", false}, {"nonsense", false},
		{"0.0.0.0/0", false}, {"10.0.0.0/8", false}, {"10.0.0.0/15", false},
	} {
		_, err := (&Provider{cfg: Config{CrewNetworkPool: tc.raw}}).crewNetworkPool()
		if (err == nil) != tc.ok {
			t.Fatalf("pool %q: err=%v ok=%v", tc.raw, err, tc.ok)
		}
		if err != nil && !errors.Is(err, errCrewNetworkPool) {
			t.Fatalf("pool error must wrap errCrewNetworkPool: %v", err)
		}
	}
}

func TestParseProcNetRoute(t *testing.T) {
	const table = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	got, err := parseProcNetRoute(bufio.NewScanner(strings.NewReader(table)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0.0.0.0/0", "172.17.0.0/16", "192.168.1.0/24"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("route %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// A crew network's own bridge route must not make the pool look taken; a LAN
// or VPN route inside the pool must (found by the integration test: the
// second crew failed on the first crew's bridge route).
func TestForeignRouteInPool(t *testing.T) {
	pool := netip.MustParsePrefix("10.239.248.0/24")
	ours := netip.MustParsePrefix("10.239.248.0/27")
	lan := netip.MustParsePrefix("10.239.248.128/25")
	def := netip.MustParsePrefix("0.0.0.0/0")
	if _, bad := foreignRouteInPool(pool, []netip.Prefix{def, ours}, []netip.Prefix{ours}); bad {
		t.Fatal("a docker bridge route inside the pool is not a conflict")
	}
	if r, bad := foreignRouteInPool(pool, []netip.Prefix{def, ours, lan}, []netip.Prefix{ours}); !bad || r != lan {
		t.Fatalf("a foreign route inside the pool must be reported, got %v %v", r, bad)
	}
}

// The route check only means something when this process provably sees the
// Docker host's routing table: the daemon's bridges must show up as routes
// (Codex review of #2767: a local socket alone, e.g. Docker Desktop, does not
// prove it).
func TestHostRoutesVisible(t *testing.T) {
	orig := runningInContainer
	t.Cleanup(func() { runningInContainer = orig })
	bridge := netip.MustParsePrefix("172.20.0.0/16")
	withBridge := []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), bridge}
	noBridge := []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("192.168.1.0/24")}
	for _, tc := range []struct {
		name      string
		host      string
		container bool
		routes    []netip.Prefix
		want      bool
	}{
		{"native, bridge routes visible", "unix:///var/run/docker.sock", false, withBridge, true},
		{"local socket but daemon in a VM (Docker Desktop)", "unix:///var/run/docker.sock", false, noBridge, false},
		{"inside a container", "unix:///var/run/docker.sock", true, withBridge, false},
		{"remote daemon", "tcp://10.0.0.5:2376", false, withBridge, false},
	} {
		runningInContainer = func() bool { return tc.container }
		p := &Provider{detected: DetectResult{Host: tc.host}}
		if got := p.hostRoutesVisible(tc.routes, []netip.Prefix{bridge}); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Stopping or removing a container must drop its warm entry, or an ensure
// within the warm TTL hands back the stopped container unchecked (found by
// the #2767 migration test).
func TestEvictWarmContainer(t *testing.T) {
	p := &Provider{}
	p.setWarm("crew-a", "abc123def456", "")
	p.setWarm("crew-b", "zzz", "")
	p.evictWarmContainer("abc123def456")
	if _, ok := p.warmHit("crew-a", ""); ok {
		t.Fatal("warm entry of the stopped container survived")
	}
	if _, ok := p.warmHit("crew-b", ""); !ok {
		t.Fatal("another crew's warm entry was dropped")
	}
	p.setWarm("crew-a", "abc123def456", "")
	p.evictWarmContainer("abc123def456"[:12])
	if _, ok := p.warmHit("crew-a", ""); ok {
		t.Fatal("a short container id must evict too")
	}
}

// First stage: crew networks only where crewshipd runs on the Docker host
// itself. In the production compose file crewshipd is a container on the
// internal instance network behind a socket proxy; it is not attached to
// crew networks, so a listed crew could not reach it (review of #2767).
// Refuse there instead of starting crews that cannot work.
func TestCrewNetworkTopologySupported(t *testing.T) {
	orig := runningInContainer
	t.Cleanup(func() { runningInContainer = orig })
	for _, tc := range []struct {
		name      string
		host      string
		container bool
		ok        bool
	}{
		{"native local socket", "unix:///var/run/docker.sock", false, true},
		{"default host", "", false, true},
		{"crewshipd in a container", "unix:///var/run/docker.sock", true, false},
		{"socket proxy (production compose)", "tcp://docker-socket-proxy:2375", true, false},
		{"remote daemon", "tcp://10.0.0.5:2376", false, false},
	} {
		runningInContainer = func() bool { return tc.container }
		p := &Provider{detected: DetectResult{Host: tc.host}}
		err := p.crewNetworkTopologySupported()
		if (err == nil) != tc.ok {
			t.Fatalf("%s: err=%v, want ok=%v", tc.name, err, tc.ok)
		}
		if err != nil && !errors.Is(err, errCrewNetworkUnsupported) {
			t.Fatalf("%s: error must wrap errCrewNetworkUnsupported: %v", tc.name, err)
		}
	}
}

// A crew network copies Internal from the instance network; when that
// network is not on the daemon there is nothing to copy, and guessing
// "not internal" could make the crew network less isolated (review of #2767).
func TestInstanceNetworkInternal(t *testing.T) {
	nets := []network.Summary{
		{Network: network.Network{Name: "inst", Internal: true}},
		{Network: network.Network{Name: "other"}},
	}
	if internal, err := instanceNetworkInternal(nets, "inst"); err != nil || !internal {
		t.Fatalf("got %v %v, want internal", internal, err)
	}
	if internal, err := instanceNetworkInternal(nets, "other"); err != nil || internal {
		t.Fatalf("got %v %v, want not internal", internal, err)
	}
	if internal, err := instanceNetworkInternal(nets, ""); err != nil || internal {
		t.Fatalf("no instance network (default bridge): got %v %v, want not internal", internal, err)
	}
	if _, err := instanceNetworkInternal(nets, "missing"); err == nil {
		t.Fatal("a missing instance network must be an error")
	}
}
