package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Slot resolution — agent > crew > workspace (PRD-CREDENTIALS-V2 §2.5b).
//
// A binding is (scope, slot) → credential. The UNIQUE index makes one scope's
// answer for a slot unambiguous; these tests pin what happens when MORE THAN
// ONE scope has an answer. Most specific wins, and the losers do not appear at
// all — an agent that receives both the crew's GH_TOKEN and the workspace's
// GH_TOKEN has not been overridden, it has been handed a coin flip.
//
// The other half of this file is the compatibility guarantee, which is the
// crux of the whole change: a credential with NO binding must keep delivering
// under its own name, byte for byte as before. Every existing workspace is in
// that state, so a regression here is not a missing feature, it is every agent
// in the fleet losing its credentials on upgrade.

// TestResolution_AgentBeatsCrewBeatsWorkspace walks the ladder in one fixture
// rather than three, so a rule that is right in isolation but wrong in
// combination (e.g. "most specific wins" implemented per-scope instead of
// per-slot) has nowhere to hide.
func TestResolution_AgentBeatsCrewBeatsWorkspace(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-ws", "github-workspace", "ws-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-crew", "github-crew", "crew-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-agent", "github-agent", "agent-secret")

	seedBinding(t, db, "rs-b-ws", e.wsID, "rs-ws", "WORKSPACE", "", "", "GH_TOKEN")

	// Only the workspace binding exists: everyone gets it, including the
	// crew-less agent — WORKSPACE scope is the one that is not narrowed.
	if got := bootEnvValues(bootCreds(t, db, e.agentA)); got["GH_TOKEN"] != "ws-secret" {
		t.Fatalf("workspace-only payload = %v, want GH_TOKEN=ws-secret", got)
	}

	seedBinding(t, db, "rs-b-crew", e.wsID, "rs-crew", "CREW", e.crewA, "", "GH_TOKEN")
	assertSingleSlot(t, db, e.agentA, "GH_TOKEN", "crew-secret", "crew binding must beat the workspace binding")
	// Crew B never had a crew binding, so it still falls back to the workspace.
	assertSingleSlot(t, db, e.agentB, "GH_TOKEN", "ws-secret", "crew A's binding must not shadow crew B's fallback")

	seedBinding(t, db, "rs-b-agent", e.wsID, "rs-agent", "AGENT", "", e.agentA, "GH_TOKEN")
	assertSingleSlot(t, db, e.agentA, "GH_TOKEN", "agent-secret", "agent binding must beat both crew and workspace")
	assertSingleSlot(t, db, e.agentB, "GH_TOKEN", "ws-secret", "agent A's binding must not reach agent B")
}

// TestResolution_UnboundCredentialStillDeliversUnderItsName is the
// compatibility guarantee, asserted through all three delivery paths.
//
// Nothing in the migration renames a row and nothing in the query requires a
// binding to exist. A crew-linked credential named CREW_TOKEN arrives as
// CREW_TOKEN, and an explicit agent_credentials grant arrives under the
// env_var_name someone chose — exactly as before this change.
func TestResolution_UnboundCredentialStillDeliversUnderItsName(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-legacy", "CREW_TOKEN", "legacy-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-grant", "GRANT_CRED", "grant-secret")
	linkCredToCrew(t, db, "rs-legacy", e.crewA)
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		VALUES ('rs-ac', ?, 'rs-grant', 'EXPLICIT_TOKEN', 0)`, e.agentA)

	for name, got := range map[string]map[string]string{
		"boot":       bootEnvValues(bootCreds(t, db, e.agentA)),
		"delegation": delegationEnvValues(delegationCreds(t, db, e.agentA)),
		"peer-query": delegationEnvValues(peerQueryCreds(t, db, e.agentA)),
	} {
		if got["CREW_TOKEN"] != "legacy-secret" {
			t.Errorf("%s: payload = %v, want the unbound crew link still delivered as CREW_TOKEN", name, got)
		}
		if got["EXPLICIT_TOKEN"] != "grant-secret" {
			t.Errorf("%s: payload = %v, want the explicit grant still delivered as EXPLICIT_TOKEN", name, got)
		}
		if len(got) != 2 {
			t.Errorf("%s: payload = %v, want exactly the two pre-existing entries and nothing invented", name, got)
		}
	}
}

// TestResolution_BindingReplacesTheNameDerivedDelivery covers the one place the
// two models meet. A credential that is BOTH crew-linked and bound must arrive
// once, under the slot — not twice, once as GH_TOKEN and once as its human
// name. Delivering the same secret under two env vars is how "the token is in
// the container" and "the tool works" quietly stop being the same statement,
// and it doubles the blast radius of a leak for no benefit.
func TestResolution_BindingReplacesTheNameDerivedDelivery(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-both", "github-acme", "acme-secret")
	linkCredToCrew(t, db, "rs-both", e.crewA)
	seedBinding(t, db, "rs-b", e.wsID, "rs-both", "CREW", e.crewA, "", "GH_TOKEN")

	boot := bootCreds(t, db, e.agentA)
	if len(boot) != 1 {
		t.Fatalf("boot payload = %+v, want one entry — the binding replaces the name-derived delivery", boot)
	}
	if boot[0].EnvVar != "GH_TOKEN" || boot[0].Value != "acme-secret" {
		t.Fatalf("boot entry = {%s=%s}, want GH_TOKEN=acme-secret", boot[0].EnvVar, boot[0].Value)
	}
}

// TestResolution_ExplicitGrantStillOutranksABinding keeps the pre-existing
// authority rule intact. An agent_credentials row is a per-agent decision
// someone made by hand, and it already suppressed the crew link for the same
// credential (including a LAPSED lease, deliberately, so a TTL cannot be
// defeated by falling back to a standing copy). A binding is a broader
// statement and must not smuggle the credential back in.
func TestResolution_ExplicitGrantStillOutranksABinding(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-cred", "github-acme", "acme-secret")
	seedBinding(t, db, "rs-b", e.wsID, "rs-cred", "CREW", e.crewA, "", "GH_TOKEN")
	execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, expires_at, lease_source)
		VALUES ('rs-ac', ?, 'rs-cred', 'LEASED_ENV', 0, ?, ?)`, e.agentA, past, leaseSourceKeeperAllow)

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if len(got) != 0 {
		t.Fatalf("payload = %v, want empty: the lapsed explicit grant is authoritative for this credential, "+
			"so the binding must not resurrect it under GH_TOKEN", got)
	}
}

// TestResolution_SecondAccountInSameScopeUnderExplicitSlot is §2.5b's honest
// boundary. One crew CAN hold two GitHub accounts — but only one of them can be
// the default `gh` identity, because `gh` reads GH_TOKEN and nothing else. The
// second must be given a slot of its own and used where the tool takes an
// explicit choice. The model has to permit that, and the invariant must not
// mistake it for a conflict: the pair is (scope, SLOT), and the slots differ.
func TestResolution_SecondAccountInSameScopeUnderExplicitSlot(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	h := newBindingHandlerForTest(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-rw", "github-acme-rw", "rw-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-ro", "github-acme-ro", "ro-secret")

	for _, body := range []string{
		`{"credential_id":"rs-rw","scope":"CREW","crew_id":"` + e.crewA + `","slot":"GH_TOKEN"}`,
		`{"credential_id":"rs-ro","scope":"CREW","crew_id":"` + e.crewA + `","slot":"GH_TOKEN_READONLY"}`,
	} {
		if rr := createBindingReq(t, h, e, body); rr.Code != http.StatusCreated {
			t.Fatalf("binding %s status = %d body=%s, want 201", body, rr.Code, rr.Body.String())
		}
	}

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if got["GH_TOKEN"] != "rw-secret" || got["GH_TOKEN_READONLY"] != "ro-secret" {
		t.Fatalf("payload = %v, want GH_TOKEN=rw-secret and GH_TOKEN_READONLY=ro-secret", got)
	}
	if len(got) != 2 {
		t.Fatalf("payload = %v, want exactly the two bound accounts", got)
	}
}

// TestResolution_UndeliverableCredentialsExcludedFromBindings keeps the binding
// source under the same filters every other source has. Each of these would
// reach the container as a live env value if it slipped through: a soft-deleted
// secret the operator believes is gone, a revoked one, and a PENDING row whose
// body is the sentinel string the agent would happily use as a token.
//
// Importantly the losing rows must NOT be replaced by a lower-specificity
// binding either: revoking crew acme's token means the crew loses GH_TOKEN, not
// that it silently inherits the workspace's account.
func TestResolution_UndeliverableCredentialsExcludedFromBindings(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-revoked", "github-revoked", "revoked-secret")
	execOrFatal(t, db, `UPDATE credentials SET status = 'REVOKED' WHERE id = 'rs-revoked'`)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-ws", "github-workspace", "ws-secret")
	seedBinding(t, db, "rs-b-crew", e.wsID, "rs-revoked", "CREW", e.crewA, "", "GH_TOKEN")
	seedBinding(t, db, "rs-b-ws", e.wsID, "rs-ws", "WORKSPACE", "", "", "GH_TOKEN")

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if _, leaked := got["GH_TOKEN"]; leaked {
		t.Fatalf("payload = %v, want no GH_TOKEN at all: the crew's bound account is revoked, and "+
			"quietly falling back to the workspace account would hand the crew an identity nobody chose", got)
	}
}

// TestResolution_DeletedCrewBindingStopsDelivering is the revocation half at
// the schema level. A binding whose crew is gone must take the delivery with
// it, rather than surviving as an orphan row that keeps a credential flowing to
// agents reparented later.
func TestResolution_DeletedCrewBindingStopsDelivering(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-cred", "github-acme", "acme-secret")
	seedBinding(t, db, "rs-b", e.wsID, "rs-cred", "CREW", e.crewA, "", "GH_TOKEN")

	execOrFatal(t, db, `DELETE FROM crews WHERE id = ?`, e.crewA)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_bindings WHERE id = 'rs-b'`).Scan(&n); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if n != 0 {
		t.Fatal("the binding outlived its crew — an orphan (scope, slot) row still claims the slot")
	}
}

// ---- API surface ----

// TestBindingAPI_ListFiltersAndDelete covers the CRUD the CLI drives. The
// tenant predicate is the load-bearing part: a list that forgot workspace_id
// would enumerate every tenant's slot map, which is a description of where
// their secrets go.
func TestBindingAPI_ListFiltersAndDelete(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	h := newBindingHandlerForTest(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-a", "github-acme", "acme-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-b", "github-globex", "globex-secret")

	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('rs-ws-other', 'Other', 'other')`)
	seedCredentialEnc(t, db, "rs-ws-other", e.userID, "rs-foreign", "github-foreign", "foreign-secret")
	seedBinding(t, db, "rs-b-foreign", "rs-ws-other", "rs-foreign", "WORKSPACE", "", "", "GH_TOKEN")

	if rr := createBindingReq(t, h, e,
		`{"credential_id":"rs-a","scope":"CREW","crew_id":"`+e.crewA+`","slot":"GH_TOKEN"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create crew binding = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := createBindingReq(t, h, e,
		`{"credential_id":"rs-b","scope":"WORKSPACE","slot":"NPM_TOKEN"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create workspace binding = %d body=%s", rr.Code, rr.Body.String())
	}

	all := decodeBindingList(t, listBindingsReq(t, h, e, ""))
	if len(all) != 2 {
		t.Fatalf("list = %+v, want exactly this workspace's two bindings", all)
	}
	for _, b := range all {
		if b.CredentialID == "rs-foreign" {
			t.Fatal("another tenant's binding appeared in the list")
		}
		if b.CredentialName == "" {
			t.Errorf("binding %s has no credential_name — the slot map is unreadable without it", b.ID)
		}
	}

	crewOnly := decodeBindingList(t, listBindingsReq(t, h, e, "crew_id="+e.crewA))
	if len(crewOnly) != 1 || crewOnly[0].Slot != "GH_TOKEN" {
		t.Fatalf("crew_id filter = %+v, want just the crew's GH_TOKEN binding", crewOnly)
	}

	// Delete, then confirm the slot is free again — a delete that only
	// hid the row would 409 the next write forever.
	del := httptest.NewRequest("DELETE", "/api/v1/credentials/bindings/"+crewOnly[0].ID, nil)
	del.SetPathValue("bindingId", crewOnly[0].ID)
	ctx := withUser(del.Context(), &AuthUser{ID: e.userID})
	del = del.WithContext(withWorkspace(ctx, e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, del)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s, want 204", rr.Code, rr.Body.String())
	}
	if rr := createBindingReq(t, h, e,
		`{"credential_id":"rs-b","scope":"CREW","crew_id":"`+e.crewA+`","slot":"GH_TOKEN"}`); rr.Code != http.StatusCreated {
		t.Fatalf("re-bind after delete = %d body=%s, want 201", rr.Code, rr.Body.String())
	}
}

// TestBindingAPI_RejectsForeignAndMalformedScopes pins the validation that
// keeps a binding from naming something outside the caller's tenant, and the
// scope/owner agreement that the CHECK constraint enforces in the schema. Both
// are 400s and not 500s: the caller can fix these.
func TestBindingAPI_RejectsForeignAndMalformedScopes(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	h := newBindingHandlerForTest(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-a", "github-acme", "acme-secret")
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('rs-ws-other', 'Other', 'other')`)
	seedCredentialEnc(t, db, "rs-ws-other", e.userID, "rs-foreign", "github-foreign", "foreign-secret")
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('rs-crew-other', 'rs-ws-other', 'X', 'x')`)

	cases := []struct {
		name string
		body string
	}{
		{"unknown scope", `{"credential_id":"rs-a","scope":"GALAXY","slot":"GH_TOKEN"}`},
		{"crew scope without crew", `{"credential_id":"rs-a","scope":"CREW","slot":"GH_TOKEN"}`},
		{"agent scope without agent", `{"credential_id":"rs-a","scope":"AGENT","slot":"GH_TOKEN"}`},
		{"workspace scope with a crew", `{"credential_id":"rs-a","scope":"WORKSPACE","crew_id":"` + e.crewA + `","slot":"GH_TOKEN"}`},
		{"empty slot", `{"credential_id":"rs-a","scope":"WORKSPACE","slot":"  "}`},
		{"slot is not an env var name", `{"credential_id":"rs-a","scope":"WORKSPACE","slot":"gh token"}`},
		{"another tenant's credential", `{"credential_id":"rs-foreign","scope":"WORKSPACE","slot":"GH_TOKEN"}`},
		{"another tenant's crew", `{"credential_id":"rs-a","scope":"CREW","crew_id":"rs-crew-other","slot":"GH_TOKEN"}`},
		{"unknown credential", `{"credential_id":"nope","scope":"WORKSPACE","slot":"GH_TOKEN"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := createBindingReq(t, h, e, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestBindingAPI_ResolveShowsWhatTheAgentWillGet exercises the read-only
// resolution view. It exists because "which account is this agent actually
// using?" was previously answerable only by starting the container and looking
// — the whole reason the fused name/env-var went unnoticed for so long.
func TestBindingAPI_ResolveShowsWhatTheAgentWillGet(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	h := newBindingHandlerForTest(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-ws", "github-workspace", "ws-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-crew", "github-crew", "crew-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-plain", "PLAIN_TOKEN", "plain-secret")
	seedBinding(t, db, "rs-b-ws", e.wsID, "rs-ws", "WORKSPACE", "", "", "GH_TOKEN")
	seedBinding(t, db, "rs-b-crew", e.wsID, "rs-crew", "CREW", e.crewA, "", "GH_TOKEN")
	linkCredToCrew(t, db, "rs-plain", e.crewA)

	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentA+"/credential-bindings", nil)
	req.SetPathValue("agentId", e.agentA)
	ctx := withUser(req.Context(), &AuthUser{ID: e.userID})
	req = req.WithContext(withWorkspace(ctx, e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveForAgent(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}

	got := map[string]resolvedSlot{}
	for _, s := range decodeResolvedSlots(t, rr) {
		got[s.Slot] = s
	}
	if got["GH_TOKEN"].CredentialName != "github-crew" || got["GH_TOKEN"].Source != bindingSourceCrew {
		t.Errorf("GH_TOKEN resolved to %+v, want the crew-scope binding on github-crew", got["GH_TOKEN"])
	}
	if got["PLAIN_TOKEN"].Source != bindingSourceCrewLink {
		t.Errorf("PLAIN_TOKEN resolved to %+v, want the legacy crew link reported as such", got["PLAIN_TOKEN"])
	}
	// Never the value, at any specificity — this route is a map, not a reveal.
	for _, secret := range []string{"ws-secret", "crew-secret", "plain-secret"} {
		if strings.Contains(rr.Body.String(), secret) {
			t.Fatalf("the resolution view leaked the decrypted value %q", secret)
		}
	}
}

// TestBindingAPI_DeletingTheCredentialFreesTheSlot covers the soft-delete gap.
// `credential delete` sets deleted_at, so the FK cascade never fires. Left
// alone, "crew acme's GH_TOKEN" would stay claimed by a credential the vault no
// longer lists, and binding the replacement account would be refused with a 409
// naming a row the user cannot see or remove. Same reasoning as #1050's
// agent_credentials cleanup, one consequence worse.
func TestBindingAPI_DeletingTheCredentialFreesTheSlot(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	bindings := newBindingHandlerForTest(t, db)
	creds := NewCredentialHandler(db, newTestLogger())
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-old", "github-old", "old-secret")
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-new", "github-new", "new-secret")
	seedBinding(t, db, "rs-b", e.wsID, "rs-old", "CREW", e.crewA, "", "GH_TOKEN")

	del := httptest.NewRequest("DELETE", "/api/v1/credentials/rs-old", nil)
	del.SetPathValue("credentialId", "rs-old")
	ctx := withUser(del.Context(), &AuthUser{ID: e.userID})
	del = del.WithContext(withWorkspace(ctx, e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	creds.Delete(rr, del)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete credential = %d body=%s, want 200", rr.Code, rr.Body.String())
	}

	if rr := createBindingReq(t, bindings, e,
		`{"credential_id":"rs-new","scope":"CREW","crew_id":"`+e.crewA+`","slot":"GH_TOKEN"}`); rr.Code != http.StatusCreated {
		t.Fatalf("re-bind GH_TOKEN after deleting the old account = %d body=%s, want 201",
			rr.Code, rr.Body.String())
	}
	if got := bootEnvValues(bootCreds(t, db, e.agentA)); got["GH_TOKEN"] != "new-secret" {
		t.Fatalf("payload = %v, want GH_TOKEN=new-secret", got)
	}
}

// TestBindingAPI_MutationsRequireManage pins the role gate. A binding decides
// which account lands in a container, so it is a manage-level act — the same
// tier as deleting a credential, not the create tier that lets a MANAGER add
// one. The routes declare roleManage as well; this asserts the handler's own
// copy, which is what still holds if the route is ever re-registered without
// its role (#809's whole premise).
func TestBindingAPI_MutationsRequireManage(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	h := newBindingHandlerForTest(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "rs-a", "github-acme", "acme-secret")

	body := `{"credential_id":"rs-a","scope":"WORKSPACE","slot":"GH_TOKEN"}`
	for _, role := range []string{"MANAGER", "MEMBER", "VIEWER"} {
		t.Run(role, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/credentials/bindings", strings.NewReader(body))
			ctx := withUser(req.Context(), &AuthUser{ID: e.userID})
			req = req.WithContext(withWorkspace(ctx, e.wsID, role))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("create as %s = %d, want 403", role, rr.Code)
			}

			del := httptest.NewRequest("DELETE", "/api/v1/credentials/bindings/x", nil)
			del.SetPathValue("bindingId", "x")
			dctx := withUser(del.Context(), &AuthUser{ID: e.userID})
			del = del.WithContext(withWorkspace(dctx, e.wsID, role))
			drr := httptest.NewRecorder()
			h.Delete(drr, del)
			if drr.Code != http.StatusForbidden {
				t.Fatalf("delete as %s = %d, want 403", role, drr.Code)
			}
		})
	}
}

// ---- helpers ----

func decodeResolvedSlots(t *testing.T, rr *httptest.ResponseRecorder) []resolvedSlot {
	t.Helper()
	var out struct {
		Slots []resolvedSlot `json:"slots"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode resolved slots: %v (body=%s)", err, rr.Body.String())
	}
	return out.Slots
}

func assertSingleSlot(t *testing.T, db *sql.DB, agentID, slot, want, why string) {
	t.Helper()
	boot := bootCreds(t, db, agentID)
	var seen []string
	for _, c := range boot {
		if c.EnvVar == slot {
			seen = append(seen, c.Value)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("%s resolved %s to %d entries (%v) — %s", agentID, slot, len(seen), seen, why)
	}
	if seen[0] != want {
		t.Fatalf("%s resolved %s to %q, want %q — %s", agentID, slot, seen[0], want, why)
	}
}

func listBindingsReq(t *testing.T, h *CredentialBindingHandler, e crewDeliveryEnv, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/v1/credentials/bindings"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest("GET", url, nil)
	ctx := withUser(req.Context(), &AuthUser{ID: e.userID})
	req = req.WithContext(withWorkspace(ctx, e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	return rr
}
