package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestRunStatusCrewAuthority(t *testing.T) {
	auth, ids := seedScope(t)
	h := NewRunStatusHandler(auth.db, quietLogger())
	execOrFatal(t, auth.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('sibling',?,'Sibling','sibling')`, ids.wsA)
	for _, tc := range []struct{ id, crew string }{{"own", ids.crewA}, {"sibling", "sibling"}, {"unattributed", ""}} {
		seedWorkItem(t, auth.db, seededWork{ID: tc.id, WorkspaceID: ids.wsA, State: "running", Generation: 1, AgentID: ids.agentA})
		execOrFatal(t, auth.db, `UPDATE work_items SET crew_id=? WHERE id=?`, tc.crew, tc.id)
		seedWorkAttempt(t, auth.db, tc.id, "run-"+tc.id, 1)
	}
	var missingReason string
	for _, tc := range []struct {
		id     string
		active bool
	}{{"missing", false}, {"sibling", false}, {"unattributed", false}, {"own", true}} {
		t.Run(tc.id, func(t *testing.T) {
			req := boundReq("GET", "/api/v1/internal/runs/run-"+tc.id+"/status", nil, scopeMaster, ids.wsA)
			req.SetPathValue("runId", "run-"+tc.id)
			req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(scopeMaster, ids.wsA, ids.crewA))
			rr := httptest.NewRecorder()
			auth.requireInternal(http.HandlerFunc(h.Status)).ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Fatalf("status=%d: %s", rr.Code, rr.Body.String())
			}
			var out runStatusResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.Active != tc.active {
				t.Fatalf("active=%v want %v", out.Active, tc.active)
			}
			if tc.id == "missing" {
				missingReason = out.Reason
			} else if !tc.active && out.Reason != missingReason {
				t.Fatalf("foreign run distinguishable from missing: %q", out.Reason)
			}
		})
	}
	// The immutable work crew, not the agent's current assignment, owns the run.
	execOrFatal(t, auth.db, `UPDATE agents SET crew_id=? WHERE id=?`, "sibling", ids.agentA)
	req := httptest.NewRequest("GET", "/api/v1/internal/runs/run-own/status?workspace_id=forged", nil)
	req = req.WithContext(crewBoundCtx1186(ids.wsA, ids.crewA))
	req.SetPathValue("runId", "run-own")
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	var out runStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Active {
		t.Fatal("trusted scope or persisted work crew was replaced")
	}
}
