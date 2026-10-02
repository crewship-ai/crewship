//go:build linux && quota_live

package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/client"
)

// Explicit opt-in: creates/removes only its owned synthetic service, never
// restarts Docker or the host and never attaches existing data volumes.
func TestLiveServiceQuotaBounds(t *testing.T) {
	if os.Getenv("CREWSHIP_LIVE_SERVICE_QUOTAS") != "1" {
		t.Fatal("set CREWSHIP_LIVE_SERVICE_QUOTAS=1 for isolated Docker quota probe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	p, err := New(ctx, Config{ContainerPrefix: "crewship-quota-probe"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.Close()
	crew := fmt.Sprintf("quota%d", time.Now().UnixNano())
	slug := "quota-probe"
	svc := provider.CrewService{Name: "probe", Image: "alpine:3", QuotaEnforced: true, Command: []string{"sh", "-c", "sleep 300"}}
	id, err := p.ensureSidecar(ctx, crew, slug, p.crewNetworkFor(crew, slug), &svc)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		if err := p.RemoveCrewServices(clean, crew, slug); err != nil {
			t.Error(err)
		}
	}()
	inspected, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = checkServiceQuotaProfile(inspected.Container.HostConfig); err != nil {
		t.Fatal(err)
	}
	script := `set -eu; test "$(cat /sys/fs/cgroup/memory.max)" = 2147483648; test "$(cat /sys/fs/cgroup/memory.swap.max)" = 0; test "$(cat /sys/fs/cgroup/pids.max)" = 512; test "$(cat /sys/fs/cgroup/cpu.max)" = "100000 100000"; if dd if=/dev/zero of=/tmp/overflow bs=1048576 count=70 2>/tmp/error; then exit 91; fi; test "$(wc -c </tmp/overflow)" -le 67108864; rm -f /tmp/overflow /tmp/error; echo quota-overflow-denied`
	output, err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("quota probe: %v %s", err, output)
	}
	if !strings.Contains(string(output), "quota-overflow-denied") {
		t.Fatal("overflow was not denied")
	}
	t.Log("live cgroup memory/swap/cpu/pids and tmpfs ENOSPC verified; persistent disk/reboot remain unverified")
}
