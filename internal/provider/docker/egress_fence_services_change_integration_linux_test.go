//go:build linux

package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The narrow path when things change or fail. The rule under test: after any
// change or error, the fenced agent reaches at most its own crew's declared
// service endpoints — never the old address of a moved service, never a
// service still sitting on the shared network, never the host or the
// internet. Each case ends with the same "nothing broader" probe.

// fenceSvcHarness builds a provider with the fence and the crew network for
// the crews in fenced, against a fresh shared network.
func fenceSvcHarness(ctx context.Context, t *testing.T, instance string, fenced []string) (*Provider, string) {
	t.Helper()
	// Not t.TempDir: crew directories end up owned by the agent's uid and
	// its strict cleanup fails the test.
	tmp, err := os.MkdirTemp("", "crewship-"+instance+"-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	sidecarPath := filepath.Join(tmp, "crewship-sidecar")
	build := exec.CommandContext(ctx, "go", "build", "-o", sidecarPath, "github.com/crewship-ai/crewship/cmd/crewship-sidecar")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sidecar: %v\n%s", err, out)
	}
	entrypointPath := filepath.Join(tmp, "entrypoint.sh")
	if err := os.WriteFile(entrypointPath, []byte("#!/bin/sh\nexec sleep infinity\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shared := instance + "-" + time.Now().Format("150405")
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	p, err := New(ctx, Config{
		RuntimeImage: "alpine:3", DefaultRuntime: "runc", Network: shared, OutputBasePath: tmp,
		SidecarBinaryPath: sidecarPath, EntrypointPath: entrypointPath, InstanceID: "inst-" + instance,
		EgressFenceCrews: fenced, CrewNetworkCrews: fenced, CrewNetworkPool: "10.239.251.0/24",
	}, nil)
	if err != nil {
		// SKIP-WAIVER(#1368): needs a live Docker daemon for real namespaces,
		// bridges and nftables.
		t.Skipf("Docker not available: %v", err)
	}
	t.Cleanup(func() {
		_, _ = p.client.NetworkRemove(context.Background(), shared, client.NetworkRemoveOptions{})
		p.Close()
	})
	if err := p.pullSidecarImage(ctx, "alpine:3"); err != nil {
		t.Fatalf("pull alpine:3: %v", err)
	}
	return p, shared
}

func cleanupFenceSvcCrew(p *Provider, team provider.CrewConfig) {
	bg := context.Background()
	_ = p.RemoveCrewServices(bg, team.ID, team.Slug)
	if cid, _, _ := p.FindCrewContainer(bg, team.ID, team.Slug); cid != "" {
		_ = p.RemoveCrewRuntime(bg, cid)
	}
	_, _ = p.client.NetworkRemove(bg, p.crewNetworkName(team.ID), client.NetworkRemoveOptions{})
}

// listener is a service command answering name on each port.
func listener(name string, ports ...string) []string {
	var parts []string
	for _, port := range ports {
		parts = append(parts, "nc -lk -p "+port+" -e echo "+name)
	}
	return []string{"sh", "-c", strings.Join(parts, " & ") + " & wait"}
}

func netIPOf(ctx context.Context, t *testing.T, p *Provider, id, netName string) string {
	t.Helper()
	insp, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ep := insp.Container.NetworkSettings.Networks[netName]
	if ep == nil {
		return ""
	}
	return ep.IPAddress.String()
}

// assertNothingBroader probes, as the agent, the paths no change may open:
// the crew network's gateway (host), metadata, a public address, and DNS.
func assertNothingBroader(ctx context.Context, t *testing.T, p *Provider, cid string, team provider.CrewConfig) {
	t.Helper()
	agent := func(cmd string) int {
		_, code := crewNetExecUser(ctx, t, p, cid, "1001", cmd)
		return code
	}
	subnet, ok, err := p.crewSubnet(ctx, team.ID, team.Slug)
	if err != nil || !ok {
		t.Fatalf("crew subnet: %v %v", ok, err)
	}
	gw := subnet.Masked().Addr().Next().String()
	for _, probe := range []string{
		"nc -w 2 " + gw + " 22 </dev/null",
		"nc -w 2 169.254.169.254 80 </dev/null",
		"nc -w 2 1.1.1.1 443 </dev/null",
		"nslookup example.com",
	} {
		if agent(probe) == 0 {
			t.Fatalf("after the change the agent got a path it must not have: %s", probe)
		}
	}
}

func reaches(ctx context.Context, t *testing.T, p *Provider, cid, target, want string) bool {
	out, _ := crewNetExecUser(ctx, t, p, cid, "1001", "nc -w 2 "+target+" </dev/null")
	return strings.TrimSpace(out) == want
}

// A service declared before an existing one shifts the existing one's fixed
// address (addresses follow the sorted names). The fence must open the new
// address and close the old one, and the runtime must learn the new address.
func TestEgressFenceServiceAddressChangeIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	p, _ := fenceSvcHarness(ctx, t, "fsvc-ipchg", []string{"ipchg"})
	team := provider.CrewConfig{
		ID: "ipchg-crew-001", Slug: "ipchg", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5,
		Services: []provider.CrewService{{Name: "kv", Image: "alpine:3", Ports: []string{"6379"}, Command: listener("kv", "6379")}},
	}
	defer cleanupFenceSvcCrew(p, team)
	cid, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	ids, err := p.EnsureCrewServices(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}
	crewNet := p.crewNetworkName(team.ID)
	oldIP := netIPOf(ctx, t, p, ids["kv"], crewNet)
	if !reaches(ctx, t, p, cid, "kv 6379", "kv") {
		t.Fatal("agent must reach kv before the change")
	}

	// "aaa" sorts first and takes kv's address. It also listens on 6379 but
	// declares only 7100: if the fence kept the old kv rule, the agent would
	// reach aaa:6379.
	team.Services = []provider.CrewService{
		{Name: "aaa", Image: "alpine:3", Ports: []string{"7100"}, Command: listener("aaa", "7100", "6379")},
		team.Services[0],
	}
	ids, err = p.EnsureCrewServices(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewServices after adding a service: %v", err)
	}
	newIP := netIPOf(ctx, t, p, ids["kv"], crewNet)
	if newIP == oldIP {
		t.Fatalf("kv kept %s; the test needs the address to move", oldIP)
	}
	if got := netIPOf(ctx, t, p, ids["aaa"], crewNet); got != oldIP {
		t.Fatalf("aaa at %s, want kv's former address %s", got, oldIP)
	}
	// Before the runtime is recreated its /etc/hosts is stale; by address
	// the fence is already current.
	if !reaches(ctx, t, p, cid, newIP+" 6379", "kv") {
		t.Fatal("the fence must open kv's new address")
	}
	if reaches(ctx, t, p, cid, oldIP+" 6379", "aaa") {
		t.Fatal("the fence kept kv's old address open (reached aaa's undeclared 6379)")
	}
	if !reaches(ctx, t, p, cid, oldIP+" 7100", "aaa") {
		t.Fatal("agent must reach the new service on its declared port")
	}
	// The running runtime's hosts entries are stale: "kv" still names the
	// old address, now aaa's, where 6379 is closed — fail closed.
	if reaches(ctx, t, p, cid, "kv 6379", "aaa") {
		t.Fatal("the stale kv entry reached aaa's undeclared port")
	}
	assertNothingBroader(ctx, t, p, cid, team)

	// A running runtime is never recreated under the agent (the drift is
	// reported); the next start after a stop rebuilds it with the new hosts.
	if err := p.StopCrewRuntime(ctx, cid); err != nil {
		t.Fatal(err)
	}
	cid2, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime after the change: %v", err)
	}
	if !reaches(ctx, t, p, cid2, "kv 6379", "kv") || !reaches(ctx, t, p, cid2, "aaa 7100", "aaa") {
		t.Fatal("after the runtime refresh, names must resolve to the new addresses")
	}
	if reaches(ctx, t, p, cid2, "aaa 6379", "aaa") {
		t.Fatal("undeclared port of a service is open after the refresh")
	}
	assertNothingBroader(ctx, t, p, cid2, team)
}

// A service that is down or cannot start: the agent loses that service and
// gains nothing; the crew stays fenced.
func TestEgressFenceServiceUnavailableIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p, _ := fenceSvcHarness(ctx, t, "fsvc-down", []string{"down"})
	team := provider.CrewConfig{
		ID: "down-crew-001", Slug: "down", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5,
		Services: []provider.CrewService{{Name: "kv", Image: "alpine:3", Ports: []string{"6379"}, Command: listener("kv", "6379")}},
	}
	defer cleanupFenceSvcCrew(p, team)
	cid, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	ids, err := p.EnsureCrewServices(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}

	// Stopped behind our back.
	if _, err := p.client.ContainerStop(ctx, ids["kv"], client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if reaches(ctx, t, p, cid, "kv 6379", "kv") {
		t.Fatal("a stopped service still answered")
	}
	assertNothingBroader(ctx, t, p, cid, team)

	// Cannot start at all: the ensure fails and the fence stays narrow.
	team.Services[0].Image = "crewship-test-does-not-exist:never"
	if _, err := p.EnsureCrewServices(ctx, team); err == nil {
		t.Fatal("EnsureCrewServices must fail for a service image that does not exist")
	}
	if _, running, _ := p.FindCrewContainer(ctx, team.ID, team.Slug); running {
		if !p.anyFenced() {
			t.Fatal("the crew runs but is no longer recorded as fenced")
		}
		assertNothingBroader(ctx, t, p, cid, team)
	}
}

// The services move from the shared network to the crew's own network and the
// move fails halfway. The fenced agent must not reach the service where it
// still is (the shared network), and the crew gets nothing else.
func TestEgressFenceServiceMoveFailureIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	p, shared := fenceSvcHarness(ctx, t, "fsvc-move", nil)
	team := provider.CrewConfig{
		ID: "move-crew-001", Slug: "move", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5,
		Services: []provider.CrewService{{Name: "kv", Image: "alpine:3", Ports: []string{"6379"}, Command: listener("kv", "6379")}},
	}
	defer cleanupFenceSvcCrew(p, team)
	// Not listed yet: services start on the shared network, no runtime.
	ids, err := p.EnsureCrewServices(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewServices on the shared network: %v", err)
	}
	sharedIP := netIPOf(ctx, t, p, ids["kv"], shared)
	if sharedIP == "" {
		t.Fatal("service not on the shared network")
	}

	// Listed now: the runtime is created fenced on the crew network.
	p.cfg.CrewNetworkCrews = []string{"move"}
	p.cfg.EgressFenceCrews = []string{"move"}
	cid, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	p.networkDisconnectHook = func(n string) error {
		if n == shared {
			return errors.New("injected detach failure")
		}
		return nil
	}
	if _, err := p.EnsureCrewServices(ctx, team); err == nil {
		t.Fatal("a failed move must fail EnsureCrewServices")
	}
	p.networkDisconnectHook = nil
	if got := netIPOf(ctx, t, p, ids["kv"], p.crewNetworkName(team.ID)); got != "" {
		t.Fatalf("after the failed move the service is still on the crew network at %s", got)
	}
	if reaches(ctx, t, p, cid, sharedIP+" 6379", "kv") {
		t.Fatal("the fenced agent reached its service on the shared network")
	}
	if reaches(ctx, t, p, cid, "kv 6379", "kv") {
		t.Fatal("kv answered by name although the move failed")
	}
	assertNothingBroader(ctx, t, p, cid, team)

	// The retry completes the move and opens exactly the service.
	if _, err := p.EnsureCrewServices(ctx, team); err != nil {
		t.Fatalf("EnsureCrewServices retry: %v", err)
	}
	if !reaches(ctx, t, p, cid, "kv 6379", "kv") {
		t.Fatal("after the retry the agent must reach kv")
	}
	assertNothingBroader(ctx, t, p, cid, team)
}
