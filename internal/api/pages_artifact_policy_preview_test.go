package api

import (
	"testing"
)

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
