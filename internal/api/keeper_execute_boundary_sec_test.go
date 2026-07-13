package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// TestSecKeeper_Request_CrewlessAgent_CannotClaimCrew is the #1057 regression.
// The crew boundary check was `if agentCrewID.Valid && agentCrewID.String !=
// body.RequestingCrewID` — a crew-LESS agent (crew_id NULL) skipped the check
// entirely, so it could set requesting_crew_id to ANY crew and mis-attribute
// the ESCALATE inbox item (TargetRole MANAGER) and the journal rows. The fix
// rejects a non-empty crew claim from a crew-less agent.
//
// Pre-fix: the crew check is skipped, the request proceeds past it. Post-fix:
// 403 before evaluation.
func TestSecKeeper_Request_CrewlessAgent_CannotClaimCrew(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, _, credID := seedKeeperFixture(t, db)

	// A crew-LESS agent in the same workspace (crew_id NULL).
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug)
		 VALUES ('crewless-1', NULL, ?, 'Loner', 'loner')`, wsID)

	h := newKeeperHandler(t, db)
	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: "crewless-1",
		RequestingCrewID:  crewID, // claims a crew it does NOT belong to
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "claim a crew i do not belong to and mis-attribute the escalation",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s; want 403 (crew-less agent must not claim a crew)",
			rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "crew boundary violation") {
		t.Errorf("body = %s, want crew boundary violation", rr.Body.String())
	}
}

// TestSecKeeper_Execute_PinsExecTargetToOwnCrewContainer is the #1016
// regression. /execute took container_id from the JSON body and, on ALLOW,
// injected the plaintext secret into THAT container. #1015 closed the
// cross-tenant case; intra-workspace a peer could still name another agent's
// container. The fix derives the exec target from the requesting agent's own
// crew container (CrewContainerName) and ignores the body value.
//
// Pre-fix: the mock records the body-supplied container. Post-fix: it records
// the server-derived crew container name.
func TestSecKeeper_Execute_PinsExecTargetToOwnCrewContainer(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "fine", RiskScore: 2,
	}}
	secrets := &mockSecretGetter{secrets: map[string]string{credID: "ghp_secret"}}
	ctr := &mockContainerExec{output: "ok", exitCode: 0, execID: "exec-x"}
	h := newKeeperHandlerWithGK(t, db, gk).WithSecrets(secrets).WithContainer(ctr)

	const attackerContainer = "peer-agent-container-DO-NOT-USE"
	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "I need to list the open pull requests",
		Command:           "gh pr list",
		ContainerID:       attackerContainer, // attacker-supplied peer container
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}

	// The exec must have targeted the requesting agent's OWN crew container
	// (derived: crewship-team-<crewSlug>), never the body-supplied one.
	want := ctr.CrewContainerName(crewID, "security-crew")
	if ctr.lastExecContainerID != want {
		t.Fatalf("exec targeted container %q, want derived %q (body value %q must be ignored)",
			ctr.lastExecContainerID, want, attackerContainer)
	}
	if ctr.lastExecContainerID == attackerContainer {
		t.Fatalf("SECRET INJECTED INTO PEER CONTAINER: exec used body-supplied %q", attackerContainer)
	}
}
