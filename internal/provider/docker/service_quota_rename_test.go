package docker

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quota"
)

func quotaTestService() provider.CrewService {
	return provider.CrewService{Name: "database", Image: "alpine:3", QuotaEnforced: true, Volumes: []provider.CrewServiceVolume{{Name: "data", Mount: "/data", QuotaBytes: 64 << 20}}}
}

func existingQuotaVolume(slug string, labelOverrides map[string]string) map[string]any {
	labels := volumeLabels(sidecarVolumeLabels(covCrewID, slug, "database", "data"))
	labels[quotaBytesLabel] = "67108864"
	labels[quotaGenerationLabel] = "1"
	for k, v := range labelOverrides {
		labels[k] = v
	}
	return map[string]any{"Name": "crewship-quota-syntheticquota", "Driver": "local", "Labels": labels,
		"Options": map[string]string{"type": "none", "o": "bind", "device": "/trusted-quota/syntheticquota"}}
}

// A crew rename changes the slug label only. Crew id, service, volume,
// capacity and generation identify the filesystem; the slug must not.
func TestQuotaVolumeSurvivesCrewRename(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
		wantErr   bool
	}{
		{name: "renamed crew reuses its volume"},
		{name: "foreign crew id denied", overrides: map[string]string{crewCrewIDLabel: "ckother0001"}, wantErr: true},
		{name: "foreign service denied", overrides: map[string]string{sidecarSvcLabel: "cache"}, wantErr: true},
		{name: "foreign volume denied", overrides: map[string]string{sidecarVolNameLabel: "logs"}, wantErr: true},
		{name: "capacity mismatch denied", overrides: map[string]string{quotaBytesLabel: "134217728"}, wantErr: true},
		{name: "generation mismatch denied", overrides: map[string]string{quotaGenerationLabel: "2"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemon := newFakeQuotaDaemon(t)
			daemon.volumes["crewship-quota-syntheticquota"] = existingQuotaVolume("old-slug", tc.overrides)
			daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
			svc := quotaTestService()
			p := newCovProvider(t, Config{QuotaCatalog: &fakeQuotaCatalog{}}, daemon.ServeHTTP)
			_, err := p.ensureSidecar(t.Context(), covCrewID, "new-slug", &svc)
			if tc.wantErr {
				if !errors.Is(err, quota.ErrDenied) || daemon.mutatingCalls() != 0 {
					t.Fatalf("foreign volume identity admitted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("renamed crew lost its quota volume: %v", err)
			}
			if len(daemon.volumeCreates) != 0 || len(daemon.containerCreates) != 1 {
				t.Fatalf("volume recreated %d / containers %d", len(daemon.volumeCreates), len(daemon.containerCreates))
			}
		})
	}
}

// After a rename the old container still holds the volume under its old
// name. It must be replaced, never run alongside a second writer.
func TestQuotaServiceRenameReplacesOldSlugContainer(t *testing.T) {
	daemon := newFakeQuotaDaemon(t)
	daemon.volumes["crewship-quota-syntheticquota"] = existingQuotaVolume("old-slug", nil)
	daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
	svc := quotaTestService()
	p := newCovProvider(t, Config{QuotaCatalog: &fakeQuotaCatalog{}}, daemon.ServeHTTP)
	daemon.containers = []map[string]any{{"Id": "old", "Names": []string{"/" + p.sidecarContainerName(covCrewID, "old-slug", "database")}, "Image": svc.Image, "State": "running",
		"Labels": sidecarContainerLabels(covCrewID, "old-slug", "database", computeSidecarSpecHash(&svc))}}
	if _, err := p.ensureSidecar(t.Context(), covCrewID, "new-slug", &svc); err != nil {
		t.Fatal(err)
	}
	if len(daemon.removed) != 1 || daemon.removed[0] != "old" || len(daemon.containerCreates) != 1 {
		t.Fatalf("old-slug writer kept alongside replacement: removed %v creates %d", daemon.removed, len(daemon.containerCreates))
	}
}
