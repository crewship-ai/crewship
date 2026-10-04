package docker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnsureCrewRuntime_PausedDoesNotMutate(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "cold", true: "warm"}[warm], func(t *testing.T) {
			cfg := covRTConfig(t)
			covCrewBindDirs(t, cfg)
			var inspect map[string]any
			if err := json.Unmarshal([]byte(covHealthyInspect(covRuntimeRef)), &inspect); err != nil {
				t.Fatal(err)
			}
			inspect["State"] = map[string]any{"Status": "paused", "Running": true, "Paused": true}
			body, err := json.Marshal(inspect)
			if err != nil {
				t.Fatal(err)
			}
			f := &covRT{listBody: covExistingList("running"), inspectBody: string(body)}
			p := f.provider(t, cfg)
			if warm {
				p.setWarm(covTeam().ID, "old-cid", covTeam().Image)
			}
			id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
			if err == nil || !strings.Contains(err.Error(), "paused") || id != "" {
				t.Errorf("paused runtime reported ready: id=%q error=%v", id, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.starts)+len(f.creates)+len(f.deletes) != 0 {
				t.Errorf("mutated intentionally paused runtime: starts=%v creates=%d deletes=%v", f.starts, len(f.creates), f.deletes)
			}
		})
	}
}
