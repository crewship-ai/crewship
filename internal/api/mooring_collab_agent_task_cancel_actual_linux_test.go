//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/work"
)

func testMooringAgentActualTaskRegistrationCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"canonical-grant-revocation", "canonical-cancel-request"} {
		t.Run(mode, func(t *testing.T) {
			withMooringAgentToolHostFixture(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, a *collaborationLiveAgent) {
				read := x.request()
				read.Method = http.MethodGet
				before := newAgentToolBufferedWriter(false)
				m.agentTool(before, read)
				if before.Code != 200 {
					t.Fatalf("original admission/read failed: %d", before.Code)
				}
				d, p := a.document, a.participant
				binding, err := d.humans.storeBinding()
				if err != nil {
					t.Fatal(err)
				}
				register := func() error {
					return d.submissions.run(x.request().Context(), binding, func(bound context.Context) error {
						return d.submissions.registerAgentTask(bound, p, d.owner)
					})
				}
				if err := register(); err != nil {
					t.Fatalf("original registration control failed: %v", err)
				}
				if mode == "canonical-grant-revocation" {
					mutation := pagesGrantCall(t, x.f.pages, http.MethodDelete, "/api/v1/pages/agent-shared-page/grants?subject_type=agent&subject="+x.f.a.AgentID+"&level=write", x.key.WorkspaceID, x.f.a.HumanUserID, "MEMBER", "agent-shared-page", "")
					if mutation.Code != 200 {
						t.Fatalf("canonical grant revoke failed: %d", mutation.Code)
					}
				} else {
					result, err := work.NewStore(x.f.rig.db).RequestCancel(t.Context(), x.workID, x.f.a.HumanUserID, "original human cancelled task")
					if err != nil || result.Outcome != work.CancelOutcomeRequested {
						t.Fatalf("canonical live cancel failed: %+v %v", result, err)
					}
					var state, phase string
					var requested bool
					if err := x.f.rig.db.QueryRow(`SELECT w.state,a.runtime_phase,a.cancel_requested_at IS NOT NULL FROM work_items w JOIN work_attempts a ON a.work_id=w.id WHERE w.id=? AND a.run_id=?`, x.workID, x.f.a.RunID).Scan(&state, &phase, &requested); err != nil || state != "running" || phase != "confirmed" || !requested {
						t.Fatalf("cancel regression no longer exercises live confirmed attempt: %s %s %v %v", state, phase, requested, err)
					}
				}
				if err := register(); err == nil {
					t.Fatal("original task registration survived canonical current-authority withdrawal")
				}
				after := newAgentToolBufferedWriter(false)
				next := x.request()
				next.Method = http.MethodGet
				m.agentTool(after, next)
				if after.Code != http.StatusForbidden {
					t.Fatalf("original document tool survived canonical current-authority withdrawal: %d", after.Code)
				}
				var operations int
				if err := x.f.rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id=?`, x.key.ArtifactID).Scan(&operations); err != nil || operations != 0 {
					t.Fatalf("denied task changed SQL operations: %d %v", operations, err)
				}
			})
		})
	}
}
