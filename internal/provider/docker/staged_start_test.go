package docker

import (
	"context"
	"testing"
)

func TestStagedStart_OptInRejectsLegacyRuntime(t *testing.T) {
	for _, state := range []string{"running", "exited"} {
		t.Run(state, func(t *testing.T) {
			cfg := covRTConfig(t)
			cfg.EgressFenceCrews = []string{covTeam().ID}
			covCrewBindDirs(t, cfg)
			f := &covRT{listBody: covExistingList(state), inspectBody: covHealthyInspect(covRuntimeRef)}
			p := f.provider(t, cfg)
			crew := covTeam()
			crew.NetworkMode = "restricted"
			if _, err := p.EnsureCrewRuntime(context.Background(), crew); err == nil {
				t.Fatal("legacy pilot adopted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.starts) != 0 || len(f.deletes) != 0 || len(f.stops) != 0 {
				t.Fatalf("legacy opt-in mutated runtime: starts=%v removes=%v stops=%v", f.starts, f.deletes, f.stops)
			}
		})
	}
}

func TestStagedStart_MissingArtifactNeverStartsImageHelper(t *testing.T) {
	cfg := covRTConfig(t)
	cfg.InstanceID = "test-installation"
	cfg.EgressFenceCrews = []string{covTeam().ID}
	f := &covRT{}
	p := f.provider(t, cfg)
	crew := covTeam()
	crew.NetworkMode = "restricted"
	if _, e := p.EnsureCrewRuntime(t.Context(), crew); e == nil {
		t.Fatal("missing trusted artifact admitted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != 0 || len(f.creates) != 0 {
		t.Fatalf("image helper started before artifact qualification: creates=%v starts=%v", f.createNames, f.starts)
	}
}
