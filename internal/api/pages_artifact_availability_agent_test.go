package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func TestArtifactProjectAvailabilityScopedToAgent(t *testing.T) {
	h, ws, owner, _ := artifactProjectFixture(t)
	mustExec(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-blocked', ?, 'Blocked', 'blocked')`, ws)
	mustExec(t, h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('agent-allowed',?,'crew-lookout','Allowed','agent-allowed')`, ws)
	mustExec(t, h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('agent-blocked',?,'crew-blocked','Blocked','agent-blocked')`, ws)
	mustExec(t, h.db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-31T00:00:00Z')`, ws)
	mustExec(t, h.db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,'crew-lookout')`, ws)
	reader := mintIngressScopedToken(t, h.db, owner, "availability-agent-reader", "pages:read")
	router, err := NewRouter(h.db, "synthetic-availability-agent-secret-32", newTestLogger(), WithInternalToken("synthetic-availability-agent-host"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	get := func(query string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/pages/artifacts/availability?"+query, nil)
		req.Header.Set("Authorization", "Bearer "+reader)
		req.Header.Set("X-Workspace-ID", ws)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		var body map[string]any
		if err := json.Unmarshal(out.Body.Bytes(), &body); err != nil {
			t.Fatalf("body %q: %v", out.Body, err)
		}
		return out.Code, body
	}
	for _, tc := range []struct {
		name, query string
		policyOff   bool
		wantCreate  any // nil: field omitted
		wantCode    string
	}{
		{name: "workspace-wide answer is unchanged", query: "", wantCreate: nil},
		{name: "allowed crew", query: "agent_id=agent-allowed", wantCreate: true},
		{name: "crew outside the allowlist", query: "agent_id=agent-blocked", wantCreate: false, wantCode: "artifact_crew_not_allowed"},
		{name: "unknown agent", query: "agent_id=" + url.QueryEscape("nope"), wantCreate: false, wantCode: "artifact_crew_not_allowed"},
		{name: "policy switched off", query: "agent_id=agent-allowed", policyOff: true, wantCreate: false, wantCode: "artifact_crew_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.policyOff {
				mustExec(t, h.db, `UPDATE workspace_artifact_creation_policy SET enabled=0`)
				t.Cleanup(func() { mustExec(t, h.db, `UPDATE workspace_artifact_creation_policy SET enabled=1`) })
			}
			code, body := get(tc.query)
			if code != 200 || body["enabled"] != true {
				t.Fatalf("status %d body %v", code, body)
			}
			if got := body["can_create"]; got != tc.wantCreate {
				t.Fatalf("can_create = %v, want %v", got, tc.wantCreate)
			}
			if got, _ := body["code"].(string); got != tc.wantCode {
				t.Fatalf("code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

func TestArtifactProjectIntentRefusalCarriesTypedCrewPolicyError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     string
		wantCrew   bool
		wantAccess bool
	}{
		{name: "crew removed from allowlist", mutate: `DELETE FROM workspace_artifact_creation_crews`, wantCrew: true, wantAccess: true},
		{name: "policy disabled", mutate: `UPDATE workspace_artifact_creation_policy SET enabled=0`, wantCrew: true, wantAccess: true},
		{name: "allowed crew is accepted", mutate: `SELECT 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			mustExec(t, f.rig.db, tc.mutate)
			err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, artifacts.ProjectIntent{SubmissionID: "explicit-create"})
			if got := errors.Is(err, artifacts.ErrCrewNotAllowed); got != tc.wantCrew {
				t.Fatalf("ErrCrewNotAllowed = %v, want %v (err %v)", got, tc.wantCrew, err)
			}
			if got := errors.Is(err, errArtifactProjectAccess); got != tc.wantAccess {
				t.Fatalf("errArtifactProjectAccess = %v, want %v (err %v)", got, tc.wantAccess, err)
			}
			if !tc.wantCrew && err != nil {
				t.Fatalf("allowed crew refused: %v", err)
			}
		})
	}
}
