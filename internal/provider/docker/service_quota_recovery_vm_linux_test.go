//go:build linux && quota_vm

package docker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/moby/moby/client"
)

// Stages are invoked by the owned guest acceptance script across real reboots.
// The caller is the host server UID1000; only the installed helper runs as root.
func TestVMQuotaServiceRecovery(t *testing.T) {
	stage := os.Getenv("CREWSHIP_QUOTA_VM_STAGE")
	if stage == "" {
		t.Fatal("owned guest staged acceptance")
	}
	if os.Geteuid() != 1000 {
		t.Fatal("acceptance must use real authenticated host UID1000")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	catalog := quota.Client{Socket: os.Getenv("CREWSHIP_QUOTA_VM_SOCKET"), Namespace: "quota-acceptance"}
	if _, err := (quota.Client{Socket: catalog.Socket, Namespace: "another-database"}).Ensure(ctx, quota.Key{Crew: "quota-vm-owned", Service: "probe", Volume: "data", Generation: 1}, 64<<20, quota.Owner{}); err == nil {
		t.Fatal("foreign database namespace admitted")
	}
	p, err := New(ctx, Config{ContainerPrefix: "crewship-quota-vm-owned", QuotaCatalog: catalog}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.client.Close()
	const crew = "quota-vm-owned"
	svc := provider.CrewService{Name: "probe", Image: "alpine:3", ControllerManaged: true, QuotaEnforced: true, Command: []string{"sh", "-c", "sleep 86400"}, Volumes: []provider.CrewServiceVolume{{Name: "data", Mount: "/data", QuotaBytes: 64 << 20}}}
	statePath := os.Getenv("CREWSHIP_QUOTA_VM_STATE")
	type state struct{ ID string }
	prior := state{}
	if stage != "create" {
		raw, e := os.ReadFile(statePath)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, &prior); e != nil {
			t.Fatal(e)
		}
	}
	if stage == "pre-controller" || stage == "stopped-pre-controller" {
		inspected, e := p.client.ContainerInspect(ctx, prior.ID, client.ContainerInspectOptions{})
		if e != nil {
			t.Fatal(e)
		}
		if inspected.Container.State.Running {
			t.Fatal("Docker restarted service before durable intent reconciliation")
		}
		if e = checkServiceRestartPolicy(inspected.Container.HostConfig, true); e != nil {
			t.Fatal(e)
		}
		if _, e = catalog.Verify(ctx, quota.Key{Crew: crew, Service: "probe", Volume: "data", Generation: 1}, 64<<20); e != nil {
			t.Fatalf("helper not ready after boot: %v", e)
		}
		return
	}
	if stage == "cleanup" {
		if err = p.RemoveCrewServices(ctx, crew, "quota-synthetic"); err != nil {
			t.Fatal(err)
		}
		if err = p.RemoveCrewServiceVolumes(ctx, crew, "quota-synthetic"); err != nil {
			t.Fatal(err)
		}
		return
	}
	if stage == "stop" {
		if err = p.StopCrewService(ctx, crew, "quota-synthetic", svc.Name); err != nil {
			t.Fatal(err)
		}
		return
	}
	id, err := p.ensureSidecar(ctx, crew, "quota-synthetic", p.crewNetworkFor(crew, "quota-synthetic"), netip.Addr{}, &svc)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "create" {
		raw, _ := json.Marshal(state{ID: id})
		if err = os.WriteFile(statePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		script := `set -eu; echo BOOT_DURABLE_QUOTA_CANARY >/data/canary; if dd if=/dev/zero of=/data/overflow bs=1048576 count=80 2>/tmp/error; then exit 91; fi; rm /data/overflow; if touch /root-unbounded 2>/tmp/root-error; then exit 92; fi`
		out, e := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", script).CombinedOutput()
		if e != nil {
			t.Fatalf("physical limits: %v %s", e, out)
		}
		if _, e = catalog.Ensure(ctx, quota.Key{Crew: crew, Service: "probe", Volume: "second", Generation: 1}, 96<<20, quota.Owner{}); e == nil {
			t.Fatal("aggregate capacity admitted overflow")
		}
	} else {
		if id != prior.ID {
			t.Fatalf("recovery duplicated/recreated stable service %s vs %s", id, prior.ID)
		}
		out, e := exec.CommandContext(ctx, "docker", "exec", id, "cat", "/data/canary").CombinedOutput()
		if e != nil || strings.TrimSpace(string(out)) != "BOOT_DURABLE_QUOTA_CANARY" {
			t.Fatalf("boot lost canary: %v %s", e, out)
		}
	}
	same, err := p.ensureSidecar(ctx, crew, "quota-synthetic", p.crewNetworkFor(crew, "quota-synthetic"), netip.Addr{}, &svc)
	if err != nil || same != id {
		t.Fatalf("reconciliation not idempotent: %s %v", same, err)
	}
	list, e := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if e != nil {
		t.Fatal(e)
	}
	owned := 0
	for _, c := range list.Items {
		if c.Labels[crewCrewIDLabel] == crew {
			owned++
			if c.Labels[crewKindLabel] != sidecarKind {
				t.Fatal("service-only recovery launched agent")
			}
		}
	}
	if owned != 1 {
		t.Fatalf("unexpected owned container count %d", owned)
	}
}
