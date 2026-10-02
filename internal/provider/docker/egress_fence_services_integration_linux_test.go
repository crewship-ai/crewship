//go:build linux

package docker

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The narrow path (#1368 + #2240): a fenced agent reaches its own crew's
// declared service by name on its exposed port, and nothing else — not
// another port of that service, not the host, not DNS. When the service's
// address changes, the fence follows: the new address opens and the old one
// closes (checked by putting a different container on the old address).
func TestEgressFenceOwnServicesIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tmp, err := os.MkdirTemp("", "crewship-fence-svc-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
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
	shared := "fence-svc-it-" + time.Now().Format("150405")
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	p, err := New(ctx, Config{
		RuntimeImage: "alpine:3", DefaultRuntime: "runc", Network: shared, OutputBasePath: tmp,
		SidecarBinaryPath: sidecarPath, EntrypointPath: entrypointPath, InstanceID: "inst-fsvc",
		EgressFenceCrews: []string{"fsvc"}, CrewNetworkCrews: []string{"fsvc"}, CrewNetworkPool: "10.239.252.0/24",
	}, nil)
	if err != nil {
		// SKIP-WAIVER(#1368): needs a live Docker daemon for real namespaces,
		// bridges and nftables.
		t.Skipf("Docker not available: %v", err)
	}
	defer p.Close()
	defer func() { _, _ = p.client.NetworkRemove(context.Background(), shared, client.NetworkRemoveOptions{}) }()
	if err := p.pullSidecarImage(ctx, "alpine:3"); err != nil {
		t.Fatalf("pull alpine:3: %v", err)
	}
	team := provider.CrewConfig{
		ID: "fsvc-crew-001", Slug: "fsvc", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5,
		Services: []provider.CrewService{{
			Name: "kv", Image: "alpine:3", Ports: []string{"6379"},
			Command: []string{"sh", "-c", "nc -lk -p 7000 -e echo undeclared & exec nc -lk -p 6379 -e echo kv"},
		}},
	}
	defer func() {
		bg := context.Background()
		_ = p.RemoveCrewServices(bg, team.ID, team.Slug)
		if cid, _, _ := p.FindCrewContainer(bg, team.ID, team.Slug); cid != "" {
			_ = p.RemoveCrewRuntime(bg, cid)
		}
		_, _ = p.client.NetworkRemove(bg, p.crewNetworkName(team.ID), client.NetworkRemoveOptions{})
	}()
	cid, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	ids, err := p.EnsureCrewServices(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewServices: %v", err)
	}
	svcIP := func(id string) string {
		insp, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return insp.Container.NetworkSettings.Networks[p.crewNetworkName(team.ID)].IPAddress.String()
	}
	oldIP := svcIP(ids["kv"])
	agent := func(cmd string) (string, int) {
		return crewNetExecUser(ctx, t, p, cid, "1001", cmd)
	}
	if out, _ := agent("nc -w 2 kv 6379 </dev/null"); strings.TrimSpace(out) != "kv" {
		t.Fatalf("agent must reach its own service by name on the declared port, got %q", out)
	}
	if out, _ := agent("nc -w 2 " + oldIP + " 7000 </dev/null"); strings.Contains(out, "undeclared") {
		t.Fatal("agent reached an undeclared port of its own service")
	}
	gw := strings.TrimSuffix(oldIP, oldIP[strings.LastIndex(oldIP, ".")+1:]) + "1"
	if _, code := agent("nc -w 2 " + gw + " 22 </dev/null"); code == 0 {
		t.Fatal("agent reached the host through the crew network gateway")
	}
	if _, code := agent("nslookup kv"); code == 0 {
		t.Fatal("agent DNS must stay blocked; service names resolve through /etc/hosts")
	}

	// Fixed addressing: the service is at .4 of the crew subnet, the runtime
	// in the dynamic upper half; a recreated service comes back at the same
	// address; no other container can be handed it.
	subnet, ok, err := p.crewSubnet(ctx, team.ID, team.Slug)
	if err != nil || !ok {
		t.Fatalf("crew subnet: %v %v", ok, err)
	}
	want, _ := serviceAddrs(subnet, []string{"kv"})
	if oldIP != want["kv"].String() {
		t.Fatalf("service at %s, want its fixed address %s", oldIP, want["kv"])
	}
	rtIP := svcIP(cid)
	if a := netip.MustParseAddr(rtIP).As4(); a[3]%32 < 16 {
		t.Fatalf("runtime got %s, want an address from the dynamic upper half", rtIP)
	}
	if err := p.RemoveCrewServices(ctx, team.ID, team.Slug); err != nil {
		t.Fatal(err)
	}
	stranger, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:3", Cmd: []string{"sleep", "60"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(p.crewNetworkName(team.ID))},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(context.Background(), stranger.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := p.client.ContainerStart(ctx, stranger.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := svcIP(stranger.ID); got == oldIP {
		t.Fatalf("a dynamic container was handed the service's fixed address %s", got)
	}
	ids2, err := p.EnsureCrewServices(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewServices after recreate: %v", err)
	}
	if got := svcIP(ids2["kv"]); got != oldIP {
		t.Fatalf("recreated service at %s, want its fixed address %s", got, oldIP)
	}
	if out, _ := agent("nc -w 2 kv 6379 </dev/null"); strings.TrimSpace(out) != "kv" {
		t.Fatalf("agent must reach the recreated service, got %q", out)
	}

	// Found live on dev2: a crew stop stops its services, and the next
	// start found the STOPPED service "not at its address" (a stopped
	// container has no live IP) and refused to start the crew.
	if err := p.StopCrewServices(ctx, team.ID, team.Slug); err != nil {
		t.Fatal(err)
	}
	ids3, err := p.EnsureCrewServices(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewServices after a stop: %v", err)
	}
	if got := svcIP(ids3["kv"]); got != oldIP {
		t.Fatalf("restarted service at %s, want %s", got, oldIP)
	}
	if out, _ := agent("nc -w 2 kv 6379 </dev/null"); strings.TrimSpace(out) != "kv" {
		t.Fatalf("agent must reach the restarted service, got %q", out)
	}
}

// crewNetExecUser runs a shell command as user in a container and returns
// stdout and exit code.
func crewNetExecUser(ctx context.Context, t *testing.T, p *Provider, cid, user, cmd string) (string, int) {
	t.Helper()
	res, err := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, Cmd: []string{"sh", "-c", cmd}, User: user + ":" + user})
	if err != nil {
		t.Fatalf("exec %q: %v", cmd, err)
	}
	b, _ := readAllAndClose(res.Reader)
	code, _, _ := p.waitExecExit(ctx, res.ExecID, 100)
	return b, code
}

func readAllAndClose(r interface {
	Read([]byte) (int, error)
	Close() error
}) (string, error) {
	defer r.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String(), nil
}
