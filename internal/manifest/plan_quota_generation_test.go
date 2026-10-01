package manifest

import (
	"strings"
	"testing"
)

// `crewship apply` on an existing crew must fail at plan time, not at the
// next service start, when a manifest changes a quota volume's capacity
// without bumping its generation.
func TestPlanRejectsQuotaResizeWithoutGenerationBump(t *testing.T) {
	stored := `[{"name":"database","image":"postgres:16","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":67108864,"generation":1}]}]`
	cases := []struct {
		name    string
		body    map[string]any
		wantErr bool
	}{
		{name: "resize without bump", body: map[string]any{"services_json": strings.Replace(stored, "67108864", "134217728", 1)}, wantErr: true},
		{name: "resize with bump", body: map[string]any{"services_json": strings.Replace(strings.Replace(stored, "67108864", "134217728", 1), `"generation":1`, `"generation":2`, 1)}},
		{name: "services not in manifest", body: map[string]any{"name": "Crew"}},
		{name: "unchanged", body: map[string]any{"services_json": stored}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := &CrewResponse{ID: "c1", Slug: "crew", ServicesJSON: &stored}
			err := checkCrewQuotaTransition(existing, tc.body)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "generation 2") {
				t.Fatalf("error does not say how to fix it: %v", err)
			}
		})
	}
}
