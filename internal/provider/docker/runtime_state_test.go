package docker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Model a stale /containers/json response followed by a newer inspect.
// Assertions cover daemon actions, not just the returned container ID.
func TestEnsureCrewRuntime_UsesInspectedState(t *testing.T) {
	for _, tc := range []struct {
		name, listed, inspected                string
		staleContract, wantStart, wantRecreate bool
	}{
		{"stopped after list", "running", "exited", false, true, false},
		{"stopped with drift", "running", "exited", true, false, true},
		{"started after list", "exited", "running", true, false, false},
		{"paused after list", "exited", "paused", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := covRTConfig(t)
			covCrewBindDirs(t, cfg)
			f := &covRT{listBody: covExistingList(tc.listed)}
			p := f.provider(t, cfg)
			labels := map[string]string{}
			if !tc.staleContract {
				labels[crewRuntimeContractLabel] = p.crewRuntimeContractDigest()
			}
			var inspect map[string]any
			if err := json.Unmarshal([]byte(covLabelledInspect(covRuntimeRef, tc.inspected == "running", labels)), &inspect); err != nil {
				t.Fatal(err)
			}
			inspect["State"] = map[string]any{"Status": tc.inspected, "Running": tc.inspected == "running" || tc.inspected == "paused", "Paused": tc.inspected == "paused"}
			body, err := json.Marshal(inspect)
			if err != nil {
				t.Fatal(err)
			}
			f.inspectBody = string(body)
			id, err := p.EnsureCrewRuntime(context.Background(), covTeam())
			if tc.inspected == "paused" {
				if err == nil || !strings.Contains(err.Error(), "paused") {
					t.Fatalf("expected actionable paused error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			startedOld := false
			for _, id := range f.starts {
				startedOld = startedOld || id == "old-cid"
			}
			// Paused containers are preserved without start or teardown.
			if startedOld != tc.wantStart {
				t.Errorf("started old container = %v, want %v (starts %v)", startedOld, tc.wantStart, f.starts)
			}
			if got := len(f.creates) > 0; got != tc.wantRecreate {
				t.Errorf("created replacement = %v, want %v", got, tc.wantRecreate)
			}
			if got := len(f.deletes) > 0; got != tc.wantRecreate {
				t.Errorf("removed old container = %v, want %v", got, tc.wantRecreate)
			}
			if !tc.wantRecreate && tc.inspected != "paused" && id != "old-cid" {
				t.Errorf("id = %q, want old-cid", id)
			}
		})
	}
}

func TestEnsureCrewRuntime_RejectsMissingInspectedState(t *testing.T) {
	for _, state := range []any{nil, map[string]any{"Status": ""}} {
		cfg := covRTConfig(t)
		f := &covRT{listBody: covExistingList("running")}
		var body map[string]any
		if err := json.Unmarshal([]byte(covHealthyInspect(covRuntimeRef)), &body); err != nil {
			t.Fatal(err)
		}
		body["State"] = state
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		f.inspectBody = string(encoded)
		p := f.provider(t, cfg)
		if _, err := p.EnsureCrewRuntime(context.Background(), covTeam()); err == nil || !strings.Contains(err.Error(), "missing state") {
			t.Fatalf("expected missing state error, got %v", err)
		}
		f.mu.Lock()
		if len(f.starts) != 0 || len(f.creates) != 0 || len(f.deletes) != 0 {
			t.Errorf("invalid inspect triggered lifecycle changes: starts=%v creates=%d deletes=%v", f.starts, len(f.creates), f.deletes)
		}
		f.mu.Unlock()
	}
}
