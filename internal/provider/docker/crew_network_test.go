package docker

import (
	"bufio"
	"errors"
	"net/netip"
	"strings"
	"testing"
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
