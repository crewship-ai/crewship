package docker

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
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
// name. It must be replaced, never run alongside a second writer, but only
// when it provably belongs to this installation: a restored or cloned
// database on the same daemon carries the same crew ids.
func TestQuotaServiceRenameReplacesOldSlugContainer(t *testing.T) {
	const self = "instance-self"
	tests := []struct {
		name        string
		instanceID  string
		prefix      string
		label       string
		wantRemoved bool
		wantErr     bool
	}{
		{name: "own old-slug container is replaced", instanceID: self, label: self, wantRemoved: true},
		{name: "another installation's container is never touched", instanceID: self, label: "instance-other"},
		{name: "unlabelled container is never touched", instanceID: self, label: ""},
		{name: "container under another prefix is never touched", instanceID: self, prefix: "otherprefix", label: self},
		{name: "empty local identity refuses replacement", instanceID: "", label: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			daemon := newFakeQuotaDaemon(t)
			daemon.volumes["crewship-quota-syntheticquota"] = existingQuotaVolume("old-slug", nil)
			daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
			svc := quotaTestService()
			p := newCovProvider(t, Config{QuotaCatalog: &fakeQuotaCatalog{}, InstanceID: tc.instanceID}, daemon.ServeHTTP)
			name := p.sidecarContainerName(covCrewID, "old-slug", "database")
			if tc.prefix != "" {
				name = tc.prefix + strings.TrimPrefix(name, p.namePrefix())
			}
			labels := sidecarContainerLabels(covCrewID, "old-slug", "database", computeSidecarSpecHash(&svc))
			if tc.label != "" {
				labels = resourcelifecycle.WithInstanceLabel(labels, tc.label)
			}
			daemon.containers = []map[string]any{{"Id": "old", "Names": []string{"/" + name}, "Image": svc.Image, "State": "running", "Labels": labels}}
			_, err := p.ensureSidecar(t.Context(), covCrewID, "new-slug", &svc)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ensureSidecar error = %v, wantErr %v", err, tc.wantErr)
			}
			removed := len(daemon.removed) == 1 && daemon.removed[0] == "old"
			if removed != tc.wantRemoved || (!tc.wantRemoved && (len(daemon.removed) != 0 || len(daemon.stopped) != 0)) {
				t.Fatalf("removed %v stopped %v, want old removed=%v", daemon.removed, daemon.stopped, tc.wantRemoved)
			}
			if tc.wantRemoved && len(daemon.containerCreates) != 1 {
				t.Fatalf("replacement not created: creates %d", len(daemon.containerCreates))
			}
		})
	}
}
