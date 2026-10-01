package docker

import (
	"testing"

	"github.com/moby/moby/api/types/container"
)

// Every quota service has exactly one restart owner. The durable controller
// only reconciles services with an intent row (ControllerManaged); without
// one, Docker's on-failure policy stays the owner, as for any other service.
func TestQuotaServiceRestartOwner(t *testing.T) {
	cases := []struct {
		name    string
		managed bool
		want    container.RestartPolicy
	}{
		{name: "unmanaged quota service keeps docker restart", want: container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3}},
		{name: "controller-managed quota service", managed: true, want: container.RestartPolicy{Name: container.RestartPolicyDisabled}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" create", func(t *testing.T) {
			daemon := newFakeQuotaDaemon(t)
			daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
			svc := quotaTestService()
			svc.ControllerManaged = tc.managed
			p := newCovProvider(t, Config{QuotaCatalog: &fakeQuotaCatalog{}}, daemon.ServeHTTP)
			if _, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", &svc); err != nil {
				t.Fatal(err)
			}
			if got := daemon.containerCreates[0].HostConfig.RestartPolicy; got != tc.want {
				t.Fatalf("restart owner %+v, want %+v", got, tc.want)
			}
		})
		t.Run(tc.name+" reuse", func(t *testing.T) {
			daemon := newFakeQuotaDaemon(t)
			daemon.volumes["crewship-quota-syntheticquota"] = existingQuotaVolume("alpha", nil)
			daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
			svc := quotaTestService()
			svc.ControllerManaged = tc.managed
			p := newCovProvider(t, Config{QuotaCatalog: &fakeQuotaCatalog{}}, daemon.ServeHTTP)
			daemon.containers = []map[string]any{{"Id": "existing", "Names": []string{"/" + p.sidecarContainerName(covCrewID, "alpha", svc.Name)}, "Image": svc.Image, "State": "running",
				"Labels": sidecarContainerLabels(covCrewID, "alpha", svc.Name, computeSidecarSpecHash(&svc))}}
			hc := quotaHostConfig()
			hc.RestartPolicy = tc.want
			daemon.hostConfigs["existing"] = hc
			id, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", &svc)
			if err != nil || id != "existing" || daemon.mutatingCalls() != 0 {
				t.Fatalf("correct restart owner treated as drift: %v id %q calls %d", err, id, daemon.mutatingCalls())
			}
		})
	}
}
