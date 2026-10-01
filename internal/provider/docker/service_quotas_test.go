package docker

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// quotaHostConfig is the HostConfig a correctly created quota service has.
func quotaHostConfig() *container.HostConfig {
	hc := &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "crewship-quota-syntheticquota", Target: "/data", VolumeOptions: &mount.VolumeOptions{NoCopy: true}}}}
	hc.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyDisabled}
	applyServiceQuotas(hc)
	return hc
}

func TestServiceQuotaActualConfigurationDrift(t *testing.T) {
	for _, field := range []string{"valid", "swap", "cpu", "memory", "pids", "logs", "tmpfs", "rootfs", "mount", "inspect_failure"} {
		t.Run(field, func(t *testing.T) {
			svc := quotaTestService()
			daemon := newFakeQuotaDaemon(t)
			daemon.volumes["crewship-quota-syntheticquota"] = existingQuotaVolume("alpha", nil)
			daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
			hc := quotaHostConfig()
			switch field {
			case "swap":
				hc.MemorySwap = 0
			case "cpu":
				hc.NanoCPUs = 0
			case "memory":
				hc.Memory = 0
			case "pids":
				hc.PidsLimit = nil
			case "logs":
				hc.LogConfig.Config = nil
			case "tmpfs":
				hc.Tmpfs = nil
			case "rootfs":
				hc.ReadonlyRootfs = false
			case "mount":
				hc.Mounts[0].Type = mount.TypeBind
			}
			p := newCovProvider(t, Config{QuotaCatalog: &fakeQuotaCatalog{}}, daemon.ServeHTTP)
			daemon.containers = []map[string]any{{"Id": "existing", "Names": []string{"/" + p.sidecarContainerName(covCrewID, "alpha", svc.Name)}, "Image": svc.Image, "State": "running",
				"Labels": sidecarContainerLabels(covCrewID, "alpha", svc.Name, computeSidecarSpecHash(&svc))}}
			if field != "inspect_failure" {
				daemon.hostConfigs["existing"] = hc
			}
			id, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", &svc)
			if field == "inspect_failure" {
				if err == nil || len(daemon.containerCreates) != 0 || len(daemon.stopped) != 0 || len(daemon.removed) != 0 {
					t.Fatalf("inspect failure was reused or mutated: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if field == "valid" {
				if id != "existing" || len(daemon.containerCreates) != 0 || len(daemon.removed) != 0 {
					t.Fatal("valid audited service not reused")
				}
				return
			}
			if id != "replacement" || len(daemon.containerCreates) != 1 || len(daemon.removed) != 1 {
				t.Fatalf("drift not replaced %q creates %d removed %v", id, len(daemon.containerCreates), daemon.removed)
			}
			if err := checkServiceQuotaProfile(daemon.containerCreates[0].HostConfig); err != nil {
				t.Errorf("replacement quota: %v", err)
			}
		})
	}
}

func TestServiceQuotaCreateBounds(t *testing.T) {
	daemon := newFakeQuotaDaemon(t)
	daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
	svc := quotaTestService()
	p := newCovProvider(t, Config{QuotaCatalog: &fakeQuotaCatalog{}}, daemon.ServeHTTP)
	if _, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", &svc); err != nil {
		t.Fatal(err)
	}
	if len(daemon.containerCreates) != 1 {
		t.Fatal("quota service not created")
	}
	hc := daemon.containerCreates[0].HostConfig
	if err := checkServiceQuotaProfile(hc); err != nil {
		t.Fatal(err)
	}
	if hc.MemorySwap != hc.Memory || hc.LogConfig.Config["max-size"] != "10m" || hc.LogConfig.Config["max-file"] != "3" || !strings.Contains(hc.Tmpfs["/tmp"], "size=67108864") || !hc.ReadonlyRootfs {
		t.Fatal("missing host-enforced limits")
	}
}

func TestManagedServicePolicyKeepsSpecAndLegacyRestart(t *testing.T) {
	svc := covRedisSvc()
	legacy := computeSidecarSpecHash(&svc)
	svc.ControllerManaged = true
	if legacy != computeSidecarSpecHash(&svc) {
		t.Fatal("controller ownership must be corrected in place, not by a spec-hash recreate")
	}
	h := captureSidecarHostConfig(t)
	if h.RestartPolicy.Name != container.RestartPolicyOnFailure || h.RestartPolicy.MaximumRetryCount != 3 {
		t.Fatal("legacy unmanaged service restart policy changed")
	}
}

func TestManagedStopUpdatesExitedPreUpgradeContainer(t *testing.T) {
	updated := false
	stopped := false
	p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[{"Id":"old","State":"exited","Labels":{"crewship.crew-id":"crew-a","crewship.kind":"sidecar","crewship.svc":"redis"}}]`))
		case strings.HasSuffix(r.URL.Path, "/containers/old/update"):
			updated = true
			var body struct{ RestartPolicy container.RestartPolicy }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.RestartPolicy.Name != container.RestartPolicyDisabled {
				t.Error("old service restart policy retained")
			}
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stopped = true
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	if err := p.StopCrewService(t.Context(), "crew-a", "crew-a", "redis"); err != nil {
		t.Fatal(err)
	}
	if !updated || stopped {
		t.Fatalf("exited service stop must disable reboot restart updated%t stopped%t", updated, stopped)
	}
}
