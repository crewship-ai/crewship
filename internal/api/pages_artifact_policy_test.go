package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func artifactPolicyCall(t *testing.T, h *PageHandler, ws, user, role, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, method, "/", ws, user, role, body)
	r.SetPathValue("workspaceId", ws)
	w := httptest.NewRecorder()
	if method == "GET" {
		h.GetArtifactCreationPolicy(w, r)
	} else {
		h.UpdateArtifactCreationPolicy(w, r)
	}
	return w
}
func enableArtifactPolicyForTest(t *testing.T, h *PageHandler, ws, user, crew string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"enabled": true, "crew_ids": []string{crew}, "base_version": 0})
	artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", string(body)), 200)
}
func TestArtifactCreationPolicyDefaultAdminScopeAndCAS(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	if h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("new workspace enabled by default")
	}
	w := artifactPolicyCall(t, h, ws, user, "OWNER", "GET", "")
	artifactExpect(t, w, 200)
	var initial artifactCreationPolicy
	if json.Unmarshal(w.Body.Bytes(), &initial) != nil || initial.Enabled || initial.Version != 0 || initial.CrewIDs == nil {
		t.Fatalf("bad default %s", w.Body.String())
	}
	pagesSeedUser(t, h, ws, "policy-member", "policy@example.com", "MEMBER")
	artifactExpect(t, artifactPolicyCall(t, h, ws, "policy-member", "OWNER", "GET", ""), 403)
	artifactExpect(t, artifactPolicyCall(t, h, ws, "policy-member", "OWNER", "PUT", `{"enabled":true,"crew_ids":["crew-lookout"],"base_version":0}`), 403)
	enableArtifactPolicyForTest(t, h, ws, user, "crew-lookout")
	if !h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("explicit crew allowlist did not enable")
	}
	artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":false,"crew_ids":[],"base_version":0}`), 409)
	if !h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("stale update revoked current policy")
	}
	artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":false,"crew_ids":["crew-lookout"],"base_version":1}`), 200)
	if h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("workspace disable ineffective")
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE workspace_id=? AND user_id=?`, ws, user)
	artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "GET", ""), 403)
	artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":true,"crew_ids":["crew-lookout"],"base_version":2}`), 403)

}
func TestArtifactCreationPolicyRejectsForeignDuplicateAndNullCrews(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	for _, body := range []string{`{}`, `{"enabled":true,"crew_ids":null,"base_version":0}`, `{"enabled":true,"crew_ids":["crew-lookout","crew-lookout"],"base_version":0}`, `{"enabled":true,"crew_ids":["missing-crew"],"base_version":0}`, `{"enabled":null,"crew_ids":[],"base_version":0}`, `{"enabled":true,"crew_ids":[],"base_version":null}`} {
		artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", body), 400)
	}
	var count int
	_ = h.db.QueryRow(`SELECT count(*) FROM workspace_artifact_creation_policy WHERE workspace_id=?`, ws).Scan(&count)
	if count != 0 {
		t.Fatal("invalid policy partially persisted")
	}
}
func TestArtifactPolicyRevocationStopsPublicationButNotHumanSourceSave(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"allowed","base_rev":0,"mutation_id":"allowed"}`), 201)
	artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":false,"crew_ids":["artifact-crew"],"base_version":1}`), 200)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"blocked","base_rev":1,"mutation_id":"blocked"}`), 403)
	artifactExpect(t, artifactSave(t, h, ws, user, "human unaffected", "human", 1), 201)
	var head int
	_ = h.db.QueryRow(`SELECT head_rev FROM page_artifacts`).Scan(&head)
	if head != 2 {
		t.Fatal("blocked agent advanced head")
	}
}

func TestArtifactPolicyBlocksNewOptInAndCreationAfterRevocation(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	artifactExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":true,"crew_ids":[],"base_version":1}`), 200)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 403)
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, creationBody), 403)
	// Revoking an already-issued human opt-in remains possible while policy is off.
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "DELETE"), 204)
	var count int
	_ = h.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='agent-created'`).Scan(&count)
	if count != 0 {
		t.Fatal("revoked policy created a Page")
	}
}

func TestArtifactPolicyRejectsForgedPathAndQuery(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			r := pagesGrantRequest(t, method, "/?workspace_id=foreign-workspace", ws, user, "OWNER", `{"enabled":true,"crew_ids":["crew-lookout"],"base_version":0}`)
			r.SetPathValue("workspaceId", "foreign-workspace")
			w := httptest.NewRecorder()
			if method == "GET" {
				h.GetArtifactCreationPolicy(w, r)
			} else {
				h.UpdateArtifactCreationPolicy(w, r)
			}
			artifactExpect(t, w, 404)
		})
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM workspace_artifact_creation_policy`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("forged scope changed policy: count=%d err=%v", count, err)
	}
}
