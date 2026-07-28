package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// Crew → agent credential fanout (PRD-CREDENTIALS-V2 §1.2, blocker #1).
//
// Before this, delivery to a container read ONLY agent_credentials. The
// credential_crews table ("this credential belongs to that crew") reached the
// UI listing and the sidecar metadata listing and stopped there — nothing ever
// fanned it out. So "assign the secret to crew 1" meant "crew 1's members can
// SEE it"; the agents in crew 1 booted without it and every real assignment
// still had to be made per agent.
//
// The two paths that hand a decrypted value to a container are:
//
//	InternalHandler.resolveAgentCredentials  — BOOT: env vars, /secrets, credstore
//	AssignmentHandler.loadAgentCredentials   — the delegation / hire boundary
//
// Both now read the same derivation (agentDeliveredCredentialsSQL): explicit
// agent_credentials grants UNION credentials linked via credential_crews to the
// agent's own crew. The tests below pin that derivation from both ends, because
// a fanout that lands in one resolver and not the other is indistinguishable
// from the bug it replaces.

// crewDeliveryEnv is the fixture every test here starts from: one workspace,
// two crews, one agent in each. Two crews (not one) so cross-crew isolation is
// checkable in every test that seeds a link, not only the one named for it.
type crewDeliveryEnv struct {
	userID string
	wsID   string
	crewA  string
	crewB  string
	agentA string
	agentB string
}

func seedCrewDeliveryEnv(t *testing.T, db *sql.DB) crewDeliveryEnv {
	t.Helper()
	ensureEncryptionKey(t)
	e := crewDeliveryEnv{
		crewA:  "cd-crew-a",
		crewB:  "cd-crew-b",
		agentA: "cd-agent-a",
		agentB: "cd-agent-b",
	}
	e.userID = seedTestUser(t, db)
	e.wsID = seedTestWorkspace(t, db, e.userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew A', 'crew-a')`, e.crewA, e.wsID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Crew B', 'crew-b')`, e.crewB, e.wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'A', 'agent-a')`, e.agentA, e.crewA, e.wsID)
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'B', 'agent-b')`, e.agentB, e.crewB, e.wsID)
	return e
}

// linkCredToCrew writes the credential_crews row that, until this change,
// bought the crew nothing but UI visibility.
func linkCredToCrew(t *testing.T, db *sql.DB, credID, crewID string) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, ?)`, credID, crewID)
}

// assignCredToAgent writes the explicit per-agent grant — the kind an operator
// created deliberately and can revoke on its own.
func assignCredToAgent(t *testing.T, db *sql.DB, credID, agentID, envVar string, priority int) {
	t.Helper()
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		"ac-"+credID+"-"+agentID, agentID, credID, envVar, priority)
}

// bootCreds runs the BOOT delivery path.
func bootCreds(t *testing.T, db *sql.DB, agentID string) []mcpCredEntry {
	t.Helper()
	creds, err := covCfgHandler(db).resolveAgentCredentials(httptest.NewRequest("GET", "/", nil), agentID)
	if err != nil {
		t.Fatalf("resolveAgentCredentials: %v", err)
	}
	return creds
}

// delegationCreds runs the sub-agent / hire delivery path.
func delegationCreds(t *testing.T, db *sql.DB, agentID string) []orchestrator.Credential {
	t.Helper()
	creds, err := NewAssignmentHandler(db, nil, nil, "tok", newTestLogger()).
		loadAgentCredentials(context.Background(), agentID)
	if err != nil {
		t.Fatalf("loadAgentCredentials: %v", err)
	}
	return creds
}

// peerQueryCreds runs the peer-query delivery path — a full agent run, and the
// third consumer of the delivery set. It was missed when #1373 fixed the other
// two (its own doc comment records being caught late for the ACTIVE filter as
// well), which is the drift the shared constant exists to end.
func peerQueryCreds(t *testing.T, db *sql.DB, agentID string) []orchestrator.Credential {
	t.Helper()
	creds, err := NewQueryHandler(db, nil, nil, "tok", newTestLogger()).
		loadAgentCredentials(context.Background(), agentID)
	if err != nil {
		t.Fatalf("query_handler loadAgentCredentials: %v", err)
	}
	return creds
}

// bootEnvValues flattens the boot payload to env → value for assertions.
func bootEnvValues(creds []mcpCredEntry) map[string]string {
	out := make(map[string]string, len(creds))
	for _, c := range creds {
		out[c.EnvVar] = c.Value
	}
	return out
}

func delegationEnvValues(creds []orchestrator.Credential) map[string]string {
	out := make(map[string]string, len(creds))
	for _, c := range creds {
		out[c.EnvVarName] = c.PlainValue
	}
	return out
}

// TestCrewDelivery_LinkedCredentialReachesCrewAgent is the headline bug: a
// credential linked to a crew must arrive in that crew's agents through BOTH
// delivery paths, under credentials.name as the env var (the convention
// autoAssignCredentials already writes into agent_credentials.env_var_name).
func TestCrewDelivery_LinkedCredentialReachesCrewAgent(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "crew-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)

	t.Run("boot", func(t *testing.T) {
		got := bootEnvValues(bootCreds(t, db, e.agentA))
		if got["CREW_TOKEN"] != "crew-secret" {
			t.Fatalf("boot payload = %v, want CREW_TOKEN=crew-secret from the crew link", got)
		}
	})
	t.Run("delegation", func(t *testing.T) {
		got := delegationEnvValues(delegationCreds(t, db, e.agentA))
		if got["CREW_TOKEN"] != "crew-secret" {
			t.Fatalf("delegation payload = %v, want CREW_TOKEN=crew-secret from the crew link", got)
		}
	})
}

// TestCrewDelivery_AgentCreatedAfterLinkReceives pins the read-time derivation.
// Agents are created at five sites (agents_create, crew_templates,
// internal_status, agents_hire, services/onboarding); a write-side fanout would
// have to be hooked into all five and would silently miss the sixth. Deriving at
// read time makes "joined the crew later" work with no hook at all.
func TestCrewDelivery_AgentCreatedAfterLinkReceives(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "crew-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)

	// Created strictly after the link, with no assignment call of any kind.
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES ('cd-late', ?, ?, 'Late', 'agent-late')`,
		e.crewA, e.wsID)

	if got := bootEnvValues(bootCreds(t, db, "cd-late")); got["CREW_TOKEN"] != "crew-secret" {
		t.Fatalf("boot payload = %v, want the later-created crew member to receive CREW_TOKEN", got)
	}
	if got := delegationEnvValues(delegationCreds(t, db, "cd-late")); got["CREW_TOKEN"] != "crew-secret" {
		t.Fatalf("delegation payload = %v, want the later-created crew member to receive CREW_TOKEN", got)
	}
}

// TestCrewDelivery_CrossCrewIsolation is a security property, not a nicety: a
// credential linked to crew A must never reach an agent whose crew is B. The
// derivation joins on the agent's OWN agents.crew_id — a join that accidentally
// widened (e.g. to the workspace) would still pass every other test here.
func TestCrewDelivery_CrossCrewIsolation(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred-a", "CREW_A_TOKEN", "crew-a-secret")
	linkCredToCrew(t, db, "cd-cred-a", e.crewA)

	if creds := bootCreds(t, db, e.agentB); len(creds) != 0 {
		t.Fatalf("boot payload for a crew-B agent = %+v, want empty — crew A's credential must not cross", creds)
	}
	if creds := delegationCreds(t, db, e.agentB); len(creds) != 0 {
		t.Fatalf("delegation payload for a crew-B agent = %+v, want empty — crew A's credential must not cross", creds)
	}
}

// TestCrewDelivery_CrewlessAgentGetsNothing covers agents.crew_id IS NULL. A
// naive join predicate (`a.crew_id = cc.crew_id` with NULL semantics misread, or
// a LEFT JOIN) would hand every crew-linked credential in the workspace to an
// unassigned agent — the widest possible leak from the narrowest input.
func TestCrewDelivery_CrewlessAgentGetsNothing(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "crew-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('cd-loose', ?, 'Loose', 'agent-loose')`, e.wsID)

	if creds := bootCreds(t, db, "cd-loose"); len(creds) != 0 {
		t.Fatalf("boot payload for a crew-less agent = %+v, want empty", creds)
	}
	if creds := delegationCreds(t, db, "cd-loose"); len(creds) != 0 {
		t.Fatalf("delegation payload for a crew-less agent = %+v, want empty", creds)
	}
}

// TestCrewDelivery_UnlinkStopsDeliveryExplicitSurvives is the revocation half.
// Deleting the credential_crews row must stop delivery on the next boot — a
// materialised fanout would have left the derived agent_credentials rows behind
// and kept delivering forever. An explicit per-agent grant is a separate
// decision and must survive the unlink untouched.
func TestCrewDelivery_UnlinkStopsDeliveryExplicitSurvives(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-crew-cred", "CREW_TOKEN", "crew-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-own-cred", "OWN_TOKEN", "own-secret")
	linkCredToCrew(t, db, "cd-crew-cred", e.crewA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('cd-ac-own', ?, 'cd-own-cred', 'OWN_TOKEN', 0)`, e.agentA)

	if got := bootEnvValues(bootCreds(t, db, e.agentA)); len(got) != 2 {
		t.Fatalf("boot payload before unlink = %v, want both the crew link and the explicit grant", got)
	}

	execOrFatal(t, db, `DELETE FROM credential_crews WHERE credential_id = 'cd-crew-cred' AND crew_id = ?`, e.crewA)

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if _, still := got["CREW_TOKEN"]; still {
		t.Fatalf("boot payload after unlink = %v, want CREW_TOKEN gone", got)
	}
	if got["OWN_TOKEN"] != "own-secret" {
		t.Fatalf("boot payload after unlink = %v, want the explicit grant untouched", got)
	}

	del := delegationEnvValues(delegationCreds(t, db, e.agentA))
	if _, still := del["CREW_TOKEN"]; still {
		t.Fatalf("delegation payload after unlink = %v, want CREW_TOKEN gone", del)
	}
	if del["OWN_TOKEN"] != "own-secret" {
		t.Fatalf("delegation payload after unlink = %v, want the explicit grant untouched", del)
	}
}

// TestCrewDelivery_ExplicitGrantWinsOverCrewLink pins the dedupe rule. With both
// an explicit grant and a crew link for the SAME credential, a UNION ALL with no
// suppression would deliver the value twice under two different env names — the
// explicit one someone chose and credentials.name — and the duplicate would
// silently shadow whatever the operator configured. The explicit grant carries
// the deliberate env_var_name / priority / lease, so it wins outright.
func TestCrewDelivery_ExplicitGrantWinsOverCrewLink(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "the-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('cd-ac', ?, 'cd-cred', 'GH_TOKEN', 7)`, e.agentA)

	boot := bootCreds(t, db, e.agentA)
	if len(boot) != 1 {
		t.Fatalf("boot payload = %+v, want exactly one entry for one credential", boot)
	}
	if boot[0].EnvVar != "GH_TOKEN" {
		t.Errorf("boot env var = %q, want the explicit grant's GH_TOKEN, not credentials.name", boot[0].EnvVar)
	}
	if boot[0].Priority != 7 {
		t.Errorf("boot priority = %d, want the explicit grant's 7", boot[0].Priority)
	}

	del := delegationCreds(t, db, e.agentA)
	if len(del) != 1 {
		t.Fatalf("delegation payload = %+v, want exactly one entry for one credential", del)
	}
	if del[0].EnvVarName != "GH_TOKEN" || del[0].Priority != 7 {
		t.Errorf("delegation entry = %+v, want the explicit grant's GH_TOKEN/7", del[0])
	}
}

// TestCrewDelivery_ExpiredLeaseDropsExplicitCrewLinkUnaffected keeps the #1373
// lease gate honest against the new source. Two separate facts:
//
//   - an explicit grant whose lease lapsed still drops out — and, because the
//     explicit grant is the authoritative binding for that credential, a crew
//     link does NOT resurrect it. Fail closed: the lease exists precisely to
//     remove a standing credential from this agent, and silently swapping it for
//     the crew's standing grant would defeat the TTL without a trace.
//   - a credential delivered purely through a crew link is unaffected by the
//     gate — credential_crews has no expires_at column at all, exactly as
//     internal_credentials.go already documents for the sidecar listing.
func TestCrewDelivery_ExpiredLeaseDropsExplicitCrewLinkUnaffected(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	past := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)

	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-leased", "LEASED_TOKEN", "leased-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-crewonly", "CREW_ONLY_TOKEN", "crew-only-secret")
	linkCredToCrew(t, db, "cd-leased", e.crewA)
	linkCredToCrew(t, db, "cd-crewonly", e.crewA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, expires_at, lease_source)
		VALUES ('cd-ac-lease', ?, 'cd-leased', 'LEASED_ENV', 0, ?, ?)`, e.agentA, past, leaseSourceKeeperAllow)

	for name, got := range map[string]map[string]string{
		"boot":       bootEnvValues(bootCreds(t, db, e.agentA)),
		"delegation": delegationEnvValues(delegationCreds(t, db, e.agentA)),
	} {
		if _, leaked := got["LEASED_ENV"]; leaked {
			t.Errorf("%s: lapsed lease delivered as LEASED_ENV", name)
		}
		if _, leaked := got["LEASED_TOKEN"]; leaked {
			t.Errorf("%s: lapsed lease re-delivered through the crew link as LEASED_TOKEN — the TTL is defeated", name)
		}
		if got["CREW_ONLY_TOKEN"] != "crew-only-secret" {
			t.Errorf("%s: payload = %v, want the unleased crew link still delivered", name, got)
		}
	}
}

// TestCrewDelivery_LiveLeaseOnExplicitGrantStillWins is the availability half of
// the test above: a lease that has NOT lapsed must still deliver, and still
// under its own env var rather than being replaced by the crew link's.
func TestCrewDelivery_LiveLeaseOnExplicitGrantStillWins(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "the-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, expires_at, lease_source)
		VALUES ('cd-ac', ?, 'cd-cred', 'LEASED_ENV', 0, ?, ?)`, e.agentA, future, leaseSourceKeeperAllow)

	boot := bootCreds(t, db, e.agentA)
	if len(boot) != 1 || boot[0].EnvVar != "LEASED_ENV" {
		t.Fatalf("boot payload = %+v, want the single live-leased LEASED_ENV entry", boot)
	}
	if boot[0].LeaseExpiresAt == "" {
		t.Error("boot entry lost its lease deadline — the sidecar credstore would treat it as standing")
	}
}

// TestCrewDelivery_ExcludesUndeliverableCredentials keeps the crew source under
// the same filters the explicit source has always had. Each of these reached the
// container as a real env value if it slipped through: a soft-deleted secret the
// operator believes is gone, a revoked one, and a PENDING row whose encrypted
// body is the sentinel string "pending_manifest" — which the agent would use as
// if it were a token.
func TestCrewDelivery_ExcludesUndeliverableCredentials(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-deleted", "DELETED_TOKEN", "deleted-secret")
	execOrFatal(t, db, `UPDATE credentials SET deleted_at = datetime('now') WHERE id = 'cd-deleted'`)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-revoked", "REVOKED_TOKEN", "revoked-secret")
	execOrFatal(t, db, `UPDATE credentials SET status = 'REVOKED' WHERE id = 'cd-revoked'`)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-pending", "PENDING_TOKEN", pendingSentinelManifest)
	execOrFatal(t, db, `UPDATE credentials SET status = 'PENDING' WHERE id = 'cd-pending'`)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-good", "GOOD_TOKEN", "good-secret")
	for _, id := range []string{"cd-deleted", "cd-revoked", "cd-pending", "cd-good"} {
		linkCredToCrew(t, db, id, e.crewA)
	}

	boot := bootEnvValues(bootCreds(t, db, e.agentA))
	del := delegationEnvValues(delegationCreds(t, db, e.agentA))
	for name, got := range map[string]map[string]string{"boot": boot, "delegation": del} {
		if len(got) != 1 || got["GOOD_TOKEN"] != "good-secret" {
			t.Errorf("%s: payload = %v, want only GOOD_TOKEN", name, got)
		}
		for env, val := range got {
			if isPendingSentinel(val) {
				t.Errorf("%s: PENDING sentinel delivered as %s", name, env)
			}
		}
	}
}

// TestCrewDelivery_PendingSentinelSkippedEvenIfActive is defence in depth,
// mirroring the in-code sentinel check the explicit path already carries: a row
// whose status was flipped back to ACTIVE while its body is still the sentinel
// must not reach the agent just because the SQL filter let it past.
func TestCrewDelivery_PendingSentinelSkippedEvenIfActive(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-sentinel", "SENTINEL_TOKEN", pendingSentinelOAuth)
	linkCredToCrew(t, db, "cd-sentinel", e.crewA)

	if creds := bootCreds(t, db, e.agentA); len(creds) != 0 {
		t.Errorf("boot payload = %+v, want the sentinel body dropped", creds)
	}
	if creds := delegationCreds(t, db, e.agentA); len(creds) != 0 {
		t.Errorf("delegation payload = %+v, want the sentinel body dropped", creds)
	}
}

// TestCrewDelivery_MultipleCrewLinksOrderedAndUsernameCarried checks the two
// details the boot payload depends on beyond presence: entries stay ordered by
// priority (explicit grants ahead of crew-derived ones at equal priority, since
// the orchestrator resolves an env-var collision by taking the first), and a
// USERPASS credential's cleartext username still rides along — the sidecar mount
// path reads it and would produce a password with no user without it.
func TestCrewDelivery_MultipleCrewLinksOrderedAndUsernameCarried(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-userpass", "DB_PASSWORD", "pw-secret")
	execOrFatal(t, db, `UPDATE credentials SET type = 'USERPASS', username = 'dbuser' WHERE id = 'cd-userpass'`)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-other", "OTHER_TOKEN", "other-secret")
	linkCredToCrew(t, db, "cd-userpass", e.crewA)
	linkCredToCrew(t, db, "cd-other", e.crewA)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-explicit", "EXPL_CRED", "expl-secret")
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('cd-ac', ?, 'cd-explicit', 'EXPL_TOKEN', 0)`, e.agentA)

	boot := bootCreds(t, db, e.agentA)
	if len(boot) != 3 {
		t.Fatalf("boot payload = %+v, want 3 entries", boot)
	}
	if boot[0].EnvVar != "EXPL_TOKEN" {
		t.Errorf("first entry = %q, want the explicit grant ahead of crew-derived ones at equal priority", boot[0].EnvVar)
	}
	var sawUserpass bool
	for _, c := range boot {
		if c.EnvVar == "DB_PASSWORD" {
			sawUserpass = true
			if c.Username != "dbuser" {
				t.Errorf("USERPASS entry username = %q, want dbuser", c.Username)
			}
			if c.Type != "USERPASS" {
				t.Errorf("USERPASS entry type = %q, want USERPASS", c.Type)
			}
		}
	}
	if !sawUserpass {
		t.Error("USERPASS crew-linked credential missing from the boot payload")
	}
}

// TestCrewDelivery_PeerQueryPathAlsoReceives closes the third delivery site.
//
// A peer query is a full agent run, so an agent answering one needs the same
// credentials it boots with. Fixing resolveAgentCredentials and the delegation
// loader while leaving this one reading agent_credentials directly would have
// produced the worst kind of half-fix: the crew's secret present at boot and at
// the sub-agent boundary, absent when a peer asks a question — a difference no
// user could explain and no existing test would catch.
func TestCrewDelivery_PeerQueryPathAlsoReceives(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "crew-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)

	if got := delegationEnvValues(peerQueryCreds(t, db, e.agentA)); got["CREW_TOKEN"] != "crew-secret" {
		t.Fatalf("peer-query payload = %v, want CREW_TOKEN=crew-secret from the crew link", got)
	}
}

// TestCrewDelivery_PeerQueryKeepsCrossCrewIsolation carries the security
// property to the third path rather than assuming the shared query implies it.
func TestCrewDelivery_PeerQueryKeepsCrossCrewIsolation(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "crew-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)

	if got := delegationEnvValues(peerQueryCreds(t, db, e.agentB)); got["CREW_TOKEN"] != "" {
		t.Fatalf("crew B agent received crew A's credential over the peer-query path: %v", got)
	}
}

// TestCrewDelivery_CrossTenantGuardHoldsWithoutTheTrigger covers the one
// predicate in agentDeliveredCredentialsSQL that nothing else reaches:
// c.workspace_id = a.workspace_id.
//
// Normally unreachable, because trg_credential_crews_workspace_check ABORTs any
// insert whose credential and crew live in different workspaces. That makes the
// SQL predicate pure defence in depth — and defence in depth with no test is a
// line somebody deletes during a refactor as "obviously redundant", after which
// the only thing standing between two tenants is a trigger nobody is looking at
// either. Verified by mutation: removing the predicate breaks no other test in
// this file.
//
// So the test removes the trigger, plants the state it was preventing, and
// asserts the query still refuses to deliver.
func TestCrewDelivery_CrossTenantGuardHoldsWithoutTheTrigger(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	// A second tenant, and a credential that belongs to it. Seeded directly:
	// seedTestUser uses a fixed email, so a second call collides on users.email.
	execOrFatal(t, db,
		`INSERT INTO workspaces (id, name, slug) VALUES ('cd-ws-other', 'Other', 'other')`)
	seedCredentialEnc(t, db, "cd-ws-other", e.userID, "cd-foreign", "FOREIGN_TOKEN", "other-tenant-secret")

	// Drop both guards, then link the foreign credential to THIS tenant's crew —
	// the exact row the trigger exists to reject.
	execOrFatal(t, db, `DROP TRIGGER IF EXISTS trg_credential_crews_workspace_check`)
	execOrFatal(t, db, `DROP TRIGGER IF EXISTS trg_credential_crews_workspace_check_upd`)
	execOrFatal(t, db,
		`INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cd-foreign', ?)`, e.crewA)

	if got := bootEnvValues(bootCreds(t, db, e.agentA)); got["FOREIGN_TOKEN"] != "" {
		t.Fatalf("another tenant's credential was delivered through a crew link: %v", got)
	}
	if got := delegationEnvValues(delegationCreds(t, db, e.agentA)); got["FOREIGN_TOKEN"] != "" {
		t.Fatalf("another tenant's credential crossed the sub-agent boundary: %v", got)
	}
	if got := delegationEnvValues(peerQueryCreds(t, db, e.agentA)); got["FOREIGN_TOKEN"] != "" {
		t.Fatalf("another tenant's credential reached the peer-query path: %v", got)
	}
}

// TestCrewDelivery_ListCredentialsShowsCrewDerived closes the last reader that
// still answered from agent_credentials alone.
//
// GET /agents/{id}/credentials is the only surface — API or CLI — that answers
// "what does this agent get?". After the fanout it answered wrongly: the agent
// demonstrably boots with the crew's credential and this endpoint said it had
// none. An operator debugging a working agent would be told the thing that is
// working isn't there, which is worse than the original bug because it looks
// authoritative.
//
// Crew-derived rows have no assignment: no assignment id, no priority anyone
// chose, no lease. They are reported with an empty id and source "crew" so the
// caller can tell a grant it can revoke from one it must unlink at the crew.
func TestCrewDelivery_ListCredentialsShowsCrewDerived(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "CREW_TOKEN", "crew-secret")
	linkCredToCrew(t, db, "cd-cred", e.crewA)

	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentA+"/credentials", nil)
	req.SetPathValue("agentId", e.agentA)
	req = req.WithContext(withWorkspace(req.Context(), e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	NewAgentHandler(db, newTestLogger()).ListCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var got []struct {
		ID          string `json:"id"`
		CredName    string `json:"credential_name"`
		EnvVarName  string `json:"env_var_name"`
		GrantSource string `json:"grant_source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var crewRow *int
	for i := range got {
		if got[i].CredName == "CREW_TOKEN" {
			crewRow = &i
		}
	}
	if crewRow == nil {
		t.Fatalf("the crew-linked credential the agent actually receives is missing from the listing: %s",
			rr.Body.String())
	}
	row := got[*crewRow]
	if row.GrantSource != "crew" {
		t.Errorf("crew-derived row source = %q, want \"crew\" — an operator must be able to tell "+
			"a revocable assignment from a crew link", row.GrantSource)
	}
	if row.ID != "" {
		t.Errorf("crew-derived row carries assignment id %q; there is no assignment row to revoke", row.ID)
	}
	if row.EnvVarName != "CREW_TOKEN" {
		t.Errorf("env var = %q, want the credential name (today's crew-derived convention)", row.EnvVarName)
	}
}

// TestCrewDelivery_ListCredentialsMarksExplicitGrants is the other half: an
// explicit assignment must still read as explicit, or the new field would be
// decorative and the UI could not offer "revoke" on the one grant it applies to.
func TestCrewDelivery_ListCredentialsMarksExplicitGrants(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-cred", "DIRECT_TOKEN", "direct-secret")
	assignCredToAgent(t, db, "cd-cred", e.agentA, "DIRECT_TOKEN", 0)

	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentA+"/credentials", nil)
	req.SetPathValue("agentId", e.agentA)
	req = req.WithContext(withWorkspace(req.Context(), e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	NewAgentHandler(db, newTestLogger()).ListCredentials(rr, req)

	var got []struct {
		ID          string `json:"id"`
		CredName    string `json:"credential_name"`
		GrantSource string `json:"grant_source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one row, got %d: %s", len(got), rr.Body.String())
	}
	if got[0].GrantSource != "explicit" || got[0].ID == "" {
		t.Errorf("explicit assignment reported as source=%q id=%q; want source=explicit with its "+
			"assignment id so the UI can offer to revoke it", got[0].GrantSource, got[0].ID)
	}
}

// TestCrewDelivery_ListCredentialsShowsBindings closes the same hole a third
// time, now for credential_bindings.
//
// The first time it was the delivery paths: three loaders, one missed. Then it
// was this listing, which knew about explicit grants and nothing else. Both
// were fixed — and then P3 added bindings as a fourth delivery source, and the
// listing was not taught about it, so `crewship agent credentials riley`
// reported only the Anthropic grant for an agent that demonstrably resolves
// GH_TOKEN through its crew's binding. Found on a live server, not by a test,
// which is the point: every one of these was invisible to the suite that
// covered the thing next to it.
//
// A binding is reported with grant_source "binding": like a crew link it has
// no assignment row to revoke, but unlike one it is removed with
// `credential unbind` rather than by unlinking the crew, and an operator has
// to be able to tell which.
func TestCrewDelivery_ListCredentialsShowsBindings(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-bound", "github-acme", "acme-secret")
	seedBinding(t, db, "cd-bind-1", e.wsID, "cd-bound", "CREW", e.crewA, "", "GH_TOKEN")

	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentA+"/credentials", nil)
	req.SetPathValue("agentId", e.agentA)
	req = req.WithContext(withWorkspace(req.Context(), e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	NewAgentHandler(db, newTestLogger()).ListCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var got []struct {
		CredName    string `json:"credential_name"`
		EnvVarName  string `json:"env_var_name"`
		GrantSource string `json:"grant_source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var row *int
	for i := range got {
		if got[i].CredName == "github-acme" {
			row = &i
		}
	}
	if row == nil {
		t.Fatalf("the bound credential the agent actually receives is missing from the listing: %s",
			rr.Body.String())
	}
	if got[*row].EnvVarName != "GH_TOKEN" {
		t.Errorf("env var = %q, want the binding's slot GH_TOKEN — the whole point of a binding "+
			"is that the slot is not the credential's name", got[*row].EnvVarName)
	}
	if got[*row].GrantSource != "binding" {
		t.Errorf("grant_source = %q, want \"binding\" — an operator removes this with "+
			"`credential unbind`, not by unlinking a crew", got[*row].GrantSource)
	}
}

// TestCrewDelivery_ListCredentialsBindingRespectsCrewIsolation carries the
// security property to the new source rather than assuming the join implies it.
func TestCrewDelivery_ListCredentialsBindingRespectsCrewIsolation(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-bound", "github-acme", "acme-secret")
	seedBinding(t, db, "cd-bind-1", e.wsID, "cd-bound", "CREW", e.crewA, "", "GH_TOKEN")

	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentB+"/credentials", nil)
	req.SetPathValue("agentId", e.agentB)
	req = req.WithContext(withWorkspace(req.Context(), e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	NewAgentHandler(db, newTestLogger()).ListCredentials(rr, req)

	if strings.Contains(rr.Body.String(), "github-acme") {
		t.Errorf("crew B's agent was listed as holding crew A's bound credential: %s", rr.Body.String())
	}
}
