package docker

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRuntimeReconcileUsesInspectInsteadOfStaleList(t *testing.T) {
	for _, tc := range []struct {
		list, observed string
		recreate       bool
	}{
		{"running", "exited", true},
		{"exited", "running", false},
	} {
		t.Run(tc.list+"_then_"+tc.observed, func(t *testing.T) {
			cfg := covRTConfig(t)
			covCrewBindDirs(t, cfg)
			var inspect map[string]any
			if err := json.Unmarshal([]byte(covResourceInspect(tc.observed == "running", 4096, 2)), &inspect); err != nil {
				t.Fatal(err)
			}
			inspect["State"].(map[string]any)["Status"] = tc.observed
			body, err := json.Marshal(inspect)
			if err != nil {
				t.Fatal(err)
			}
			f := &covRT{listBody: covExistingList(tc.list), inspectBody: string(body)}
			p := f.provider(t, cfg)
			id, err := p.EnsureCrewRuntime(context.Background(), covSizedTeam(8192, 2))
			if err != nil {
				t.Fatal(err)
			}
			if (id != "old-cid") != tc.recreate {
				t.Fatalf("list=%s inspect=%s returned %s; recreate=%v", tc.list, tc.observed, id, tc.recreate)
			}
		})
	}
}
