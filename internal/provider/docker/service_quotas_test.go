package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
)

func TestServiceQuotaActualConfigurationDrift(t *testing.T) {
	for _, field := range []string{"valid", "swap", "cpu", "memory", "pids", "logs", "tmpfs", "inspect_failure", "managed_restart"} {
		t.Run(field, func(t *testing.T) {
			svc := provider.CrewService{Name: "redis", Image: "redis:7"}
			if field == "managed_restart" {
				svc.ControllerManaged = true
			}
			hash := computeSidecarSpecHash(&svc)
			hc := &container.HostConfig{}
			applyServiceQuotas(hc)
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
			}
			created, stopped, removed := false, false, false
			p := newCovProvider(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				switch {
				case strings.HasSuffix(path, "/containers/json"):
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "existing", "Names": []string{"/crewship-svc-alpha-ckalpha0001-redis"}, "Image": svc.Image, "State": "running", "Labels": map[string]string{sidecarSpecHashLabel: hash}}})
				case strings.HasSuffix(path, "/containers/existing/json"):
					if field == "inspect_failure" {
						http.Error(w, "unavailable", 500)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "existing", "HostConfig": hc})
				case strings.HasSuffix(path, "/containers/existing/stop"):
					stopped = true
					w.WriteHeader(204)
				case r.Method == http.MethodDelete && strings.HasSuffix(path, "/containers/existing"):
					removed = true
					w.WriteHeader(204)
				case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
					_ = json.NewEncoder(w).Encode(map[string]string{"Id": "image"})
				case strings.HasSuffix(path, "/images/create"):
					_, _ = w.Write([]byte("{}"))
				case strings.HasSuffix(path, "/containers/create"):
					created = true
					var req container.CreateRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if field == "managed_restart" && req.HostConfig.RestartPolicy.Name != container.RestartPolicyDisabled {
						t.Error("managed service replacement still restarts autonomously")
					}
					if err := checkServiceQuotas(req.HostConfig); err != nil {
						t.Errorf("replacement quota: %v", err)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"Id": "replacement"})
				case strings.HasSuffix(path, "/containers/replacement/start"):
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request %s %s", r.Method, path)
					w.WriteHeader(500)
				}
			})
			id, err := p.ensureSidecar(context.Background(), covCrewID, "alpha", &svc)
			if field == "inspect_failure" {
				if err == nil || created || stopped || removed {
					t.Fatalf("inspect failure was reused or mutated: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if field == "valid" {
				if id != "existing" || created || stopped || removed {
					t.Fatal("valid audited service not reused")
				}
			} else if id != "replacement" || !created || !stopped || !removed {
				t.Fatalf("drift not replaced %q %t %t %t", id, created, stopped, removed)
			}
		})
	}
}
func TestServiceQuotaCreateBounds(t *testing.T) {
	hc := captureSidecarHostConfig(t)
	if err := checkServiceQuotas(hc); err != nil {
		t.Fatal(err)
	}
	if hc.MemorySwap != hc.Memory || hc.LogConfig.Config["max-size"] != "10m" || hc.LogConfig.Config["max-file"] != "3" || !strings.Contains(hc.Tmpfs["/tmp"], "size=67108864") {
		t.Fatal("missing host-enforced limits")
	}
}

func TestManagedServicePolicyChangesSpecAndPreservesLegacy(t *testing.T) {
	svc := covRedisSvc()
	legacy := computeSidecarSpecHash(&svc)
	svc.ControllerManaged = true
	if legacy == computeSidecarSpecHash(&svc) {
		t.Fatal("managed restart policy missing from immutable desired spec")
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
				t.Fatal("old service restart policy retained")
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
