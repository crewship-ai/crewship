//go:build linux

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
	"github.com/crewship-ai/crewship/internal/quota"
)

func TestLivePersistentQuotaService(t *testing.T) {
	if os.Getenv("CREWSHIP_LIVE_PERSISTENT_QUOTAS") != "1" || os.Geteuid() != 0 {
		t.Skip("requires explicit isolated root guest Docker acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := quota.NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	p, err := New(ctx, Config{ContainerPrefix: "crewship-quota-owned", QuotaCatalog: b}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.Close()
	crew := fmt.Sprintf("owned%d", time.Now().UnixNano())
	svc := provider.CrewService{Name: "probe", Image: "alpine:3", ControllerManaged: true, QuotaEnforced: true, Command: []string{"sh", "-c", "sleep 300"}, Volumes: []provider.CrewServiceVolume{{Name: "data", Mount: "/data", QuotaBytes: 64 << 20}}}
	defer func() {
		clean, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		if err := p.RemoveCrewServices(clean, crew, "synthetic"); err != nil {
			t.Error(err)
		}
		if err := p.RemoveCrewServiceVolumes(clean, crew, "synthetic"); err != nil {
			t.Error(err)
		}
	}()
	id, err := p.ensureSidecar(ctx, crew, "synthetic", &svc)
	if err != nil {
		t.Fatal(err)
	}
	script := `set -eu; echo DURABLE_PROVIDER_CANARY >/data/canary; if touch /unbounded-root 2>/tmp/root-error; then exit 90; fi; if dd if=/dev/zero of=/data/overflow bs=1048576 count=80 2>/tmp/disk-error; then exit 91; fi; test "$(wc -c </data/overflow)" -lt 67108864; rm /data/overflow; echo physical-overflow-denied`
	out, err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", script).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "physical-overflow-denied") {
		t.Fatalf("physical service: %v %s", err, out)
	}
	same, err := p.ensureSidecar(ctx, crew, "synthetic", &svc)
	if err != nil || same != id {
		t.Fatalf("idempotent audit: %s %v", same, err)
	}
	if err = p.StopCrewService(ctx, crew, "synthetic", svc.Name); err != nil {
		t.Fatal(err)
	}
	b.Close()
	b, err = quota.NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Recover(); err != nil {
		t.Fatal(err)
	}
	p.cfg.QuotaCatalog = b
	same, err = p.ensureSidecar(ctx, crew, "synthetic", &svc)
	if err != nil || same != id {
		t.Fatalf("recovered service: %s %v", same, err)
	}
	out, err = exec.CommandContext(ctx, "docker", "exec", id, "cat", "/data/canary").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "DURABLE_PROVIDER_CANARY" {
		t.Fatalf("recovery canary: %v %s", err, out)
	}
	t.Log("actual provider bounded bind volume, readonly root, ENOSPC, idempotent mount audit and helper recovery verified")
}
