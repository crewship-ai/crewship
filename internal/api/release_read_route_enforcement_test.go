package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// TestReadFenceProbesEnforceWorkspaceScope is the runtime companion to
// TestEveryReadRouteDeclaresItsWorkspaceScope. The declaration scan covers the
// full read registration table; this test drives the 59 seeded GET probes in
// the fence table through the real router with a member of workspace A naming
// workspace B's row. A predicate removed from a covered handler therefore
// fails with the route and operation that leaked, rather than leaving a
// declaration-only green test. It intentionally does not claim that every
// read registration has a seeded behavioral probe.
//
// The probe table is deliberately shared with the broader cross-workspace
// fence. Each resource kind owns its seed, positive control, and route table;
// this test selects every GET probe from that table and never invents IDs or
// calls handlers directly.
func TestReadFenceProbesEnforceWorkspaceScope(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	attacker := fenceSeedTenant(t, db, "read-a")
	victim := fenceSeedTenant(t, db, "read-b")
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithOutputBasePath(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	r.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	r.PipelinesHandler.SetWebhookStore(pipeline.NewWebhookStore(db))
	r.PipelinesHandler.SetRunner(fenceNoopRunner{})

	probes := 0
	for _, kind := range fenceKinds() {
		kind := kind
		for _, probe := range kind.probes {
			probe := probe
			if probe.method != http.MethodGet {
				continue
			}
			for _, variant := range []struct {
				name  string
				graft bool
			}{
				{name: "direct"},
				{name: "graft", graft: true},
			} {
				if variant.graft && fenceOwnedPlaceholders(probe.path) < 2 {
					continue
				}
				path, ok := fenceSubst(probe.path, attacker, victim, variant.graft)
				if !ok {
					t.Fatalf("%s %s: probe placeholder has no seeded ID", probe.method, probe.path)
				}
				probes++
				t.Run(kind.name+"/"+variant.name+"/"+probe.path, func(t *testing.T) {
					rr := fenceDo(t, r, attacker, probe, path)
					if rr.Code >= 500 {
						t.Fatalf("GET %s returned %d before a workspace decision; body=%s", path, rr.Code, fenceTrim(rr.Body.String()))
					}
					if probe.mode == probeDeny && rr.Code >= 200 && rr.Code < 300 {
						t.Fatalf("LEAKED: workspace A GET %s returned %d for workspace B's row; body=%s", path, rr.Code, fenceTrim(rr.Body.String()))
					}
					if probe.mode == probeNoLeak && strings.Contains(rr.Body.String(), victim.marker(probe.markerFor(kind.name))) {
						t.Fatalf("LEAKED: workspace A GET %s returned workspace B marker; body=%s", path, fenceTrim(rr.Body.String()))
					}
				})
			}
		}
	}
	if probes == 0 {
		t.Fatal("read enforcement probe table is empty")
	}
}
