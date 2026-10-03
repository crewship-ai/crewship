package docker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestEnsureCrewRuntimeUsesInspectedStateAfterListSnapshot(t *testing.T) {
	for _, currentContract := range []bool{false, true} {
		name := "drifted"
		if currentContract {
			name = "current"
		}
		t.Run(name, func(t *testing.T) {
			cfg := covRTConfig(t)
			covCrewBindDirs(t, cfg)
			f := &covRT{listBody: covExistingList(string(container.StateRunning))}
			p := f.provider(t, cfg)
			labels := map[string]string{"managed-by": "crewship", "crewship.kind": "crew"}
			if currentContract {
				labels[crewRuntimeContractLabel] = p.crewRuntimeContractDigest()
			}
			var inspect map[string]any
			if err := json.Unmarshal([]byte(covLabelledInspect(covRuntimeRef, false, labels)), &inspect); err != nil {
				t.Fatal(err)
			}
			inspect["State"] = map[string]any{"Status": "exited", "Running": false, "ExitCode": 0}
			body, err := json.Marshal(inspect)
			if err != nil {
				t.Fatal(err)
			}
			f.inspectBody = string(body)
			id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if currentContract {
				if id != "old-cid" || len(f.creates) != 0 || len(f.deletes) != 0 {
					t.Fatalf("current runtime replaced: id=%s creates=%d deletes=%v", id, len(f.creates), f.deletes)
				}
				started := false
				for _, s := range f.starts {
					started = started || s == "old-cid"
				}
				if !started {
					t.Fatal("exited runtime returned without starting")
				}
			} else {
				if id == "old-cid" || len(f.creates) == 0 {
					t.Fatalf("stopped drifted runtime was not replaced: id=%s creates=%d", id, len(f.creates))
				}
			}
		})
	}
}

func TestEnsureCrewRuntimeDoesNotDestroyNewlyLiveRuntimeFromStaleStoppedList(t *testing.T) {
	for _, state := range []string{"running", "paused"} {
		t.Run(state, func(t *testing.T) {
			cfg := covRTConfig(t)
			covCrewBindDirs(t, cfg)
			f := &covRT{listBody: covExistingList(string(container.StateExited))}
			p := f.provider(t, cfg)
			var inspect map[string]any
			if err := json.Unmarshal([]byte(covLabelledInspect(covRuntimeRef, true, map[string]string{"crewship.kind": "crew"})), &inspect); err != nil {
				t.Fatal(err)
			}
			inspect["State"] = map[string]any{"Status": state, "Running": true, "Paused": state == "paused"}
			body, err := json.Marshal(inspect)
			if err != nil {
				t.Fatal(err)
			}
			f.inspectBody = string(body)
			if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.deletes) != 0 || len(f.creates) != 0 {
				t.Fatalf("destroyed live %s runtime: deletes=%v creates=%d", state, f.deletes, len(f.creates))
			}
		})
	}
}
