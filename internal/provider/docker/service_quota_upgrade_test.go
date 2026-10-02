package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// preQuotaSpecHash is a frozen copy of computeSidecarSpecHash as released
// before service quotas (#2703). Containers created by that release carry
// this digest; the upgrade must keep recognising it for services that do not
// opt in to quota enforcement.
func preQuotaSpecHash(svc *provider.CrewService) string {
	envKeys := make([]string, 0, len(svc.Env))
	for k := range svc.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	envPairs := make([][2]string, 0, len(envKeys))
	for _, k := range envKeys {
		envPairs = append(envPairs, [2]string{k, svc.Env[k]})
	}
	type volume struct{ Name, Mount string }
	vols := make([]volume, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		vols = append(vols, volume{v.Name, v.Mount})
	}
	sort.Slice(vols, func(i, j int) bool {
		if vols[i].Name != vols[j].Name {
			return vols[i].Name < vols[j].Name
		}
		return vols[i].Mount < vols[j].Mount
	})
	data, _ := json.Marshal(struct {
		Command     []string
		Env         [][2]string
		Ports       []string
		Volumes     []volume
		Healthcheck *provider.CrewServiceHealthcheck
	}{svc.Command, envPairs, svc.Ports, vols, svc.Healthcheck})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// preQuotaHostConfig is the HostConfig the pre-quota release created.
func preQuotaHostConfig(mounts []mount.Mount) *container.HostConfig {
	pids := int64(512)
	return &container.HostConfig{
		Mounts:        mounts,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3},
		SecurityOpt:   []string{"no-new-privileges:true"},
		LogConfig:     container.LogConfig{Type: "journald"}, // operator's daemon default
		Resources:     container.Resources{Memory: sidecarMemoryBytes, NanoCPUs: sidecarNanoCPUs, PidsLimit: &pids},
	}
}

func TestLegacyServiceContainerSurvivesQuotaUpgrade(t *testing.T) {
	postgres := provider.CrewService{Name: "postgres", Image: "postgres:16", Env: map[string]string{"POSTGRES_PASSWORD": "x"}, Ports: []string{"5432"}}
	cases := []struct {
		name        string
		svc         provider.CrewService
		mounts      []mount.Mount
		wantUpdated bool
	}{
		{name: "named volume service", svc: covRedisSvc(), mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "crewship-svc-alpha-ckalpha0001-vol-data", Target: "/data"}}},
		// The image declares VOLUME /var/lib/postgresql/data and the service
		// has no volumes entry: Docker made an anonymous volume that a
		// recreate would orphan, leaving an empty database.
		{name: "image-declared anonymous volume", svc: postgres, mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "4f1c0anonymous", Target: "/var/lib/postgresql/data"}}},
		{name: "controller-managed service", svc: func() provider.CrewService { s := covRedisSvc(); s.ControllerManaged = true; return s }(), wantUpdated: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemon := newFakeQuotaDaemon(t)
			svc := tc.svc
			p := newCovProvider(t, Config{}, daemon.ServeHTTP)
			daemon.containers = []map[string]any{{"Id": "legacy", "Names": []string{"/" + p.sidecarContainerName(covCrewID, "alpha", svc.Name)}, "Image": svc.Image, "State": "running",
				"Labels": sidecarContainerLabels(covCrewID, "alpha", svc.Name, preQuotaSpecHash(&svc))}}
			daemon.hostConfigs["legacy"] = preQuotaHostConfig(tc.mounts)
			id, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", p.crewNetworkFor(covCrewID, "alpha"), netip.Addr{}, &svc)
			if err != nil {
				t.Fatal(err)
			}
			if id != "legacy" || len(daemon.containerCreates) != 0 || len(daemon.stopped) != 0 || len(daemon.removed) != 0 {
				t.Fatalf("legacy container recreated by upgrade: id %q creates %d stopped %v removed %v", id, len(daemon.containerCreates), daemon.stopped, daemon.removed)
			}
			if tc.wantUpdated {
				if len(daemon.updated) != 1 || daemon.hostConfigs["legacy"].RestartPolicy.Name != container.RestartPolicyDisabled {
					t.Fatalf("managed restart policy not corrected in place: %v", daemon.updated)
				}
			} else if len(daemon.updated) != 0 {
				t.Fatalf("unmanaged legacy container mutated: %v", daemon.updated)
			}
		})
	}
}

// Non-opt-in services keep the pre-quota create profile: no /tmp tmpfs
// charged to memory and hiding the image's /tmp, no forced log driver, no
// swap override.
func TestLegacyServiceCreateKeepsPreQuotaProfile(t *testing.T) {
	hc := captureSidecarHostConfig(t)
	if len(hc.Tmpfs) != 0 || hc.LogConfig.Type != "" || len(hc.LogConfig.Config) != 0 || hc.MemorySwap != 0 || hc.ReadonlyRootfs {
		t.Fatalf("quota profile forced on a legacy service: tmpfs %v log %+v swap %d", hc.Tmpfs, hc.LogConfig, hc.MemorySwap)
	}
	if hc.Memory != sidecarMemoryBytes || hc.NanoCPUs != sidecarNanoCPUs || hc.PidsLimit == nil || *hc.PidsLimit != 512 {
		t.Fatal("pre-quota resource caps lost")
	}
	if hc.RestartPolicy.Name != container.RestartPolicyOnFailure || hc.RestartPolicy.MaximumRetryCount != 3 {
		t.Fatal("legacy restart policy changed")
	}
}

func TestLegacyServiceSpecHashUnchanged(t *testing.T) {
	for _, svc := range []provider.CrewService{covRedisSvc(), {Name: "bare", Image: "alpine:3"}, func() provider.CrewService { s := covRedisSvc(); s.ControllerManaged = true; return s }()} {
		if got, want := computeSidecarSpecHash(&svc), preQuotaSpecHash(&svc); got != want {
			t.Fatalf("%s: legacy spec hash changed %s != %s", svc.Name, got, want)
		}
	}
	q := quotaTestService()
	if computeSidecarSpecHash(&q) == preQuotaSpecHash(&q) {
		t.Fatal("quota policy missing from the opt-in spec hash")
	}
}
