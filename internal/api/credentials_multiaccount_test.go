package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// Multi-account — ten crews, ten GitHub accounts (PRD-CREDENTIALS-V2 §2.5b).
//
// The bug these tests pin: credentials.name was BOTH the human identity of the
// credential AND the environment variable the agent reads
// (recipes.go — "EnvVarName … Doubles as the credential name inside the
// workspace per existing convention"), and it carries UNIQUE(workspace_id,
// name). Two GitHub accounts in one workspace would therefore both have to be
// called GH_TOKEN, and the second INSERT is rejected by the schema. Not
// awkward — impossible.
//
// The fix splits the two ideas apart. credentials.name stays the human name of
// ONE ACCOUNT (github-acme, github-globex) and keeps its UNIQUE, which is the
// right constraint for a human name. The env var moves onto a BINDING —
// (scope, slot) → credential — where scope is workspace | crew | agent and slot
// is the env var name. Ten crews then bind ten different accounts to the same
// slot, and each crew's agents boot with their own value under GH_TOKEN.

// bindingEnv extends the crew-delivery fixture with a helper for seeding
// bindings straight into the table, so the delivery assertions do not depend on
// the HTTP handler being correct as well.
func seedBinding(t *testing.T, db *sql.DB, id, wsID, credID, scope, crewID, agentID, slot string) {
	t.Helper()
	var crew, agent any
	if crewID != "" {
		crew = crewID
	}
	if agentID != "" {
		agent = agentID
	}
	execOrFatal(t, db, `INSERT INTO credential_bindings
		(id, workspace_id, credential_id, scope, crew_id, agent_id, slot)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, credID, scope, crew, agent, slot)
}

// TestMultiAccount_TenGitHubAccountsCoexistInOneWorkspace is the headline
// proof, and it asserts BOTH halves — because only asserting the first would
// pass on today's schema and prove nothing.
//
//   - The old workaround is genuinely impossible: ten accounts cannot all be
//     named GH_TOKEN. That INSERT must still fail, since UNIQUE(workspace_id,
//     name) is correct for a human name and we are not removing it.
//   - Ten accounts with ten distinct human names coexist, and each is a real
//     GitHub credential. Under the old convention their env var would have been
//     "github-acme" — a name `gh` never reads — so coexisting was worthless.
func TestMultiAccount_TenGitHubAccountsCoexistInOneWorkspace(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	// The workaround the old model forced on the user, and why it fails.
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-first", "GH_TOKEN", "first-secret")
	enc, err := encryption.Encrypt("second-secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('ma-second', ?, 'GH_TOKEN', ?, 'SECRET', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
		e.wsID, enc, e.userID)
	if err == nil {
		t.Fatal("a second credential named GH_TOKEN was accepted — UNIQUE(workspace_id, name) is gone, " +
			"which is not the fix: the human name must stay unique, the ENV VAR is what moves to the binding")
	}

	// Ten distinct accounts, same provider, one workspace.
	for i := 0; i < 10; i++ {
		seedCredentialEnc(t, db, e.wsID, e.userID,
			fmt.Sprintf("ma-acct-%d", i), fmt.Sprintf("github-acct-%d", i), fmt.Sprintf("secret-%d", i))
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND provider = 'GITHUB' AND name LIKE 'github-acct-%'`,
		e.wsID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 10 {
		t.Fatalf("GitHub accounts in the workspace = %d, want 10", n)
	}
}

// TestMultiAccount_TenCrewsEachReceiveTheirOwnGHToken is the use case itself.
// Ten crews, ten accounts, one slot name. Every crew's agent must receive ITS
// OWN value under GH_TOKEN — not the first one, not the last one written, and
// not ten entries it has to disambiguate.
func TestMultiAccount_TenCrewsEachReceiveTheirOwnGHToken(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	ensureEncryptionKey(t)

	const crews = 10
	for i := 0; i < crews; i++ {
		crewID := fmt.Sprintf("ma-crew-%d", i)
		agentID := fmt.Sprintf("ma-agent-%d", i)
		credID := fmt.Sprintf("ma-cred-%d", i)
		execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
			crewID, wsID, fmt.Sprintf("Crew %d", i), fmt.Sprintf("crew-%d", i))
		execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, ?, ?)`,
			agentID, crewID, wsID, fmt.Sprintf("Agent %d", i), fmt.Sprintf("agent-%d", i))
		seedCredentialEnc(t, db, wsID, userID, credID, fmt.Sprintf("github-acct-%d", i), fmt.Sprintf("token-%d", i))
		seedBinding(t, db, fmt.Sprintf("ma-bind-%d", i), wsID, credID, "CREW", crewID, "", "GH_TOKEN")
	}

	for i := 0; i < crews; i++ {
		agentID := fmt.Sprintf("ma-agent-%d", i)
		want := fmt.Sprintf("token-%d", i)

		boot := bootCreds(t, db, agentID)
		if len(boot) != 1 {
			t.Fatalf("agent %d boot payload = %+v, want exactly one entry — one slot resolves to one credential", i, boot)
		}
		if boot[0].EnvVar != "GH_TOKEN" || boot[0].Value != want {
			t.Errorf("agent %d boot entry = {%s=%s}, want GH_TOKEN=%s", i, boot[0].EnvVar, boot[0].Value, want)
		}
		if got := delegationEnvValues(delegationCreds(t, db, agentID)); got["GH_TOKEN"] != want {
			t.Errorf("agent %d delegation payload = %v, want GH_TOKEN=%s", i, got, want)
		}
		if got := delegationEnvValues(peerQueryCreds(t, db, agentID)); got["GH_TOKEN"] != want {
			t.Errorf("agent %d peer-query payload = %v, want GH_TOKEN=%s", i, got, want)
		}
	}
}

// TestMultiAccount_CrossCrewIsolationThroughBindings re-asserts the security
// property through the NEW path instead of assuming the old test covers it. A
// binding join that widened from "this crew" to "this workspace" — the easiest
// mistake to make while adding the WORKSPACE scope — would still pass every
// test above, because each agent would simply see ten GH_TOKEN candidates and
// the assertion "len == 1" is the only thing standing in front of it.
func TestMultiAccount_CrossCrewIsolationThroughBindings(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-a", "github-acme", "acme-secret")
	seedBinding(t, db, "ma-bind-a", e.wsID, "ma-a", "CREW", e.crewA, "", "GH_TOKEN")

	for name, got := range map[string]map[string]string{
		"boot":       bootEnvValues(bootCreds(t, db, e.agentB)),
		"delegation": delegationEnvValues(delegationCreds(t, db, e.agentB)),
		"peer-query": delegationEnvValues(peerQueryCreds(t, db, e.agentB)),
	} {
		if len(got) != 0 {
			t.Errorf("%s: crew B agent received %v — crew A's bound account must not cross", name, got)
		}
	}
}

// TestMultiAccount_CrossTenantBindingNeverDelivers covers the workspace
// predicate on the binding join. A WORKSPACE-scope binding has no crew or agent
// to constrain it, so credential_bindings.workspace_id is the ONLY thing
// keeping another tenant's row out of this agent's payload. Planted directly,
// because no handler will write it.
func TestMultiAccount_CrossTenantBindingNeverDelivers(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ma-ws-other', 'Other', 'other')`)
	seedCredentialEnc(t, db, "ma-ws-other", e.userID, "ma-foreign", "github-foreign", "other-tenant-secret")
	seedBinding(t, db, "ma-bind-foreign", "ma-ws-other", "ma-foreign", "WORKSPACE", "", "", "GH_TOKEN")

	if got := bootEnvValues(bootCreds(t, db, e.agentA)); len(got) != 0 {
		t.Fatalf("another tenant's WORKSPACE binding was delivered: %v", got)
	}
}

// ---- The invariant: one slot, one credential, per scope ----

// TestMultiAccount_DuplicateSlotInSameScopeRejectedBySchema pins the invariant
// at the storage layer. Without a UNIQUE index ON THE BINDING, "crew acme's
// GH_TOKEN" becomes a question with two answers and delivery picks one by
// whatever the query planner felt like — the definition of a silent
// last-write-wins.
func TestMultiAccount_DuplicateSlotInSameScopeRejectedBySchema(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-a", "github-acme", "acme-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-b", "github-globex", "globex-secret")

	cases := []struct {
		name             string
		scope            string
		crewID, agentID  string
		firstID, dupeID  string
		wantSameScopeErr bool
	}{
		{name: "crew scope", scope: "CREW", crewID: e.crewA, firstID: "ma-b1", dupeID: "ma-b2", wantSameScopeErr: true},
		{name: "agent scope", scope: "AGENT", agentID: e.agentA, firstID: "ma-b3", dupeID: "ma-b4", wantSameScopeErr: true},
		// WORKSPACE has a NULL crew_id AND a NULL agent_id. SQLite treats
		// NULLs as distinct in a UNIQUE index, so a naive
		// UNIQUE(workspace_id, scope, crew_id, agent_id, slot) would let every
		// workspace-scope duplicate through — the one scope with no owning row
		// is the one where the guard silently evaporates.
		{name: "workspace scope", scope: "WORKSPACE", firstID: "ma-b5", dupeID: "ma-b6", wantSameScopeErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seedBinding(t, db, tc.firstID, e.wsID, "ma-a", tc.scope, tc.crewID, tc.agentID, "GH_TOKEN")
			var crew, agent any
			if tc.crewID != "" {
				crew = tc.crewID
			}
			if tc.agentID != "" {
				agent = tc.agentID
			}
			_, err := db.Exec(`INSERT INTO credential_bindings
				(id, workspace_id, credential_id, scope, crew_id, agent_id, slot)
				VALUES (?, ?, 'ma-b', ?, ?, ?, 'GH_TOKEN')`,
				tc.dupeID, e.wsID, tc.scope, crew, agent)
			if err == nil && tc.wantSameScopeErr {
				t.Fatalf("%s: a second binding for GH_TOKEN in the same scope was accepted — "+
					"the slot now resolves to two credentials", tc.name)
			}
		})
	}
}

// TestMultiAccount_DuplicateSlotReturns409 is the same invariant at the API
// boundary. 409 and not 200: a conflicting write must be REFUSED, so the caller
// learns that crew acme's GH_TOKEN is already spoken for. Silently replacing
// the previous binding would repoint every agent in the crew at a different
// GitHub account with no request having said so.
func TestMultiAccount_DuplicateSlotReturns409(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	h := newBindingHandlerForTest(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-a", "github-acme", "acme-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-b", "github-globex", "globex-secret")

	first := createBindingReq(t, h, e, `{"credential_id":"ma-a","scope":"CREW","crew_id":"`+e.crewA+`","slot":"GH_TOKEN"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first binding status = %d body=%s, want 201", first.Code, first.Body.String())
	}

	dupe := createBindingReq(t, h, e, `{"credential_id":"ma-b","scope":"CREW","crew_id":"`+e.crewA+`","slot":"GH_TOKEN"}`)
	if dupe.Code != http.StatusConflict {
		t.Fatalf("conflicting binding status = %d body=%s, want 409", dupe.Code, dupe.Body.String())
	}

	// And the original must still be the one that delivers — a 409 that had
	// already written the row would be worse than a 200.
	if got := bootEnvValues(bootCreds(t, db, e.agentA)); got["GH_TOKEN"] != "acme-secret" {
		t.Fatalf("after the refused write the payload = %v, want the ORIGINAL acme-secret", got)
	}
}

// TestMultiAccount_SameSlotDifferentScopesIsNotAConflict guards the other
// direction: the invariant is per SCOPE, not global. If the 409 fired on slot
// alone, binding GH_TOKEN in ten different crews — the whole point — would be
// rejected on the second crew.
func TestMultiAccount_SameSlotDifferentScopesIsNotAConflict(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	h := newBindingHandlerForTest(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-a", "github-acme", "acme-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "ma-b", "github-globex", "globex-secret")

	for _, body := range []string{
		`{"credential_id":"ma-a","scope":"CREW","crew_id":"` + e.crewA + `","slot":"GH_TOKEN"}`,
		`{"credential_id":"ma-b","scope":"CREW","crew_id":"` + e.crewB + `","slot":"GH_TOKEN"}`,
		`{"credential_id":"ma-a","scope":"WORKSPACE","slot":"GH_TOKEN"}`,
		`{"credential_id":"ma-b","scope":"AGENT","agent_id":"` + e.agentB + `","slot":"GH_TOKEN"}`,
	} {
		if rr := createBindingReq(t, h, e, body); rr.Code != http.StatusCreated {
			t.Fatalf("binding %s status = %d body=%s, want 201", body, rr.Code, rr.Body.String())
		}
	}
}

// ---- helpers ----

// newBindingHandlerForTest builds the handler with an encryption key present —
// the binding routes never decrypt, but seedCredentialEnc in the same test does.
func newBindingHandlerForTest(t *testing.T, db *sql.DB) *CredentialBindingHandler {
	t.Helper()
	ensureEncryptionKey(t)
	return NewCredentialBindingHandler(db,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
}

// createBindingReq drives POST /api/v1/credentials/bindings with an OWNER
// context. Returns the recorder so each caller asserts its own status.
func createBindingReq(t *testing.T, h *CredentialBindingHandler, e crewDeliveryEnv, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/credentials/bindings", bytes.NewBufferString(body))
	ctx := withUser(req.Context(), &AuthUser{ID: e.userID})
	ctx = withWorkspace(ctx, e.wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func decodeBindingList(t *testing.T, rr *httptest.ResponseRecorder) []credentialBindingResponse {
	t.Helper()
	var out struct {
		Bindings []credentialBindingResponse `json:"bindings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode binding list: %v (body=%s)", err, rr.Body.String())
	}
	return out.Bindings
}

// TestMultiAccount_CrossTenantGuardHoldsWithoutTheTriggers covers the one
// predicate on the binding arm that nothing else reaches:
// c.workspace_id = rb.workspace_id.
//
// trg_credential_bindings_workspace_check ABORTs any binding whose credential
// and scope live in different workspaces, so the SQL predicate is pure defence
// in depth — and defence in depth with no test is a line deleted in a refactor
// as obviously redundant, after which the only thing between two tenants is a
// trigger nobody is watching either. Verified by mutation: removing the
// predicate breaks NO test in this package, including
// TestMultiAccount_CrossTenantBindingNeverDelivers, which the triggers already
// satisfy before delivery is ever consulted.
//
// This is the same hole the crew arm had (see
// TestCrewDelivery_CrossTenantGuardHoldsWithoutTheTrigger) — the pattern is
// worth recognising: a guard the schema makes unreachable is a guard no
// ordinary test can fail on.
func TestMultiAccount_CrossTenantGuardHoldsWithoutTheTriggers(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	// A second tenant holding a credential of its own. Seeded directly:
	// seedTestUser uses a fixed email, so a second call collides.
	execOrFatal(t, db,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ma-ws-other', 'Other', 'other')`)
	seedCredentialEnc(t, db, "ma-ws-other", e.userID, "ma-foreign", "FOREIGN_TOKEN", "other-tenant-secret")

	// Drop the guards, then bind the foreign credential into THIS tenant's crew
	// under a slot its agents read — precisely the row the triggers reject.
	execOrFatal(t, db, `DROP TRIGGER IF EXISTS trg_credential_bindings_workspace_check`)
	execOrFatal(t, db, `DROP TRIGGER IF EXISTS trg_credential_bindings_workspace_check_upd`)
	seedBinding(t, db, "ma-bind-foreign", e.wsID, "ma-foreign", "CREW", e.crewA, "", "GH_TOKEN")

	for _, tc := range []struct {
		name string
		got  map[string]string
	}{
		{"boot", bootEnvValues(bootCreds(t, db, e.agentA))},
		{"delegation", delegationEnvValues(delegationCreds(t, db, e.agentA))},
		{"peer query", delegationEnvValues(peerQueryCreds(t, db, e.agentA))},
	} {
		if tc.got["GH_TOKEN"] == "other-tenant-secret" {
			t.Errorf("%s: another tenant's credential was delivered through a binding: %v", tc.name, tc.got)
		}
	}
}

// TestMultiAccount_CrewLinkSurvivesALosingBinding is the HIGH bug the merge
// review caught. A credential that is crew-linked AND has a binding that LOSES
// its slot to a more specific binding was delivered nowhere: the crew-link arm
// suppressed it because it had an applicable binding, and the binding arm only
// delivers the winner. So an operator who crew-links a secret and later adds an
// unrelated binding for it silently loses it from the container.
func TestMultiAccount_CrewLinkSurvivesALosingBinding(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	// X is crew-linked to crew A, and bound X→S at WORKSPACE scope.
	seedCredentialEnc(t, db, e.wsID, e.userID, "cred-x", "X_NAME", "x-secret")
	linkCredToCrew(t, db, "cred-x", e.crewA)
	seedBinding(t, db, "b-x-ws", e.wsID, "cred-x", "WORKSPACE", "", "", "SHARED_SLOT")

	// Y wins SHARED_SLOT with a more specific CREW binding.
	seedCredentialEnc(t, db, e.wsID, e.userID, "cred-y", "Y_NAME", "y-secret")
	seedBinding(t, db, "b-y-crew", e.wsID, "cred-y", "CREW", e.crewA, "", "SHARED_SLOT")

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if got["SHARED_SLOT"] != "y-secret" {
		t.Errorf("SHARED_SLOT = %q, want y-secret (Y's CREW binding beats X's WORKSPACE binding)", got["SHARED_SLOT"])
	}
	// X lost the slot but is still crew-linked, so it must arrive under its
	// crew-link name. Delivered-nowhere is the bug.
	if got["X_NAME"] != "x-secret" {
		t.Errorf("X_NAME = %q, want x-secret — X lost SHARED_SLOT but its crew link is independent "+
			"of that binding and must still deliver", got["X_NAME"])
	}
}

// TestMultiAccount_ListingMatchesDeliveryForCrewLinkedAndBound is the MEDIUM
// bug: a credential that is both crew-linked and bound is listed twice by
// `agent credentials` while delivery yields it once, so the listing shows a
// phantom env var no container ever sets. The listing's own comment claims it
// mirrors the delivery query; this pins that it does.
func TestMultiAccount_ListingMatchesDeliveryForCrewLinkedAndBound(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)

	seedCredentialEnc(t, db, e.wsID, e.userID, "cred-x", "SHARED_SECRET", "x-secret")
	linkCredToCrew(t, db, "cred-x", e.crewA)
	seedBinding(t, db, "b-x", e.wsID, "cred-x", "CREW", e.crewA, "", "GH_TOKEN")

	// Delivery is the truth: X arrives once, under the binding slot.
	deliv := bootEnvValues(bootCreds(t, db, e.agentA))
	if deliv["GH_TOKEN"] != "x-secret" || deliv["SHARED_SECRET"] != "" {
		t.Fatalf("delivery = %v, want only GH_TOKEN=x-secret (the crew link is shadowed by the binding)", deliv)
	}

	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentA+"/credentials", nil)
	req.SetPathValue("agentId", e.agentA)
	req = req.WithContext(withWorkspace(req.Context(), e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	NewAgentHandler(db, newTestLogger()).ListCredentials(rr, req)

	var got []struct {
		CredName   string `json:"credential_name"`
		EnvVarName string `json:"env_var_name"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	n := 0
	for _, r := range got {
		if r.CredName == "SHARED_SECRET" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("credential SHARED_SECRET listed %d times, want 1 — the crew-link row is a phantom "+
			"when a binding already delivers the same credential. Rows: %s", n, rr.Body.String())
	}
}
