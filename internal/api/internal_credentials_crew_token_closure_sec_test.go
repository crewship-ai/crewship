package api

// Internal security audit (pre-1.0) of the #1031 "fail-open" note in
// internal_credentials.go — GET /api/v1/internal/credentials.
//
// The note claimed full closure was impossible because it "needs crew-bound
// internal tokens (not just crew_id, which any caller with a valid
// X-Internal-Token can forge)". That blocker is GONE: crew-bound tokens
// shipped in #1159 (internaltoken.DeriveCrewToken, crwv1.<ws>.<crew>.<mac>),
// every crew run's sidecar is issued one (orchestrator.sidecarIPCToken),
// requireInternal validates the MAC and pins the crew in the request context,
// and ListCredentials reads the crew from THAT context in preference to the
// query. So the enumerate-a-sibling-crew path is closed — a caller can neither
// omit crew_id to widen the listing nor forge one.
//
// What existed before this file: TestListCredentials_CrewBoundContextScopes
// pins the handler half by setting ctxInternalTokenCrew BY HAND, and
// TestListCredentials_NonLoopbackWithoutCrewID_Warns only pins that a WARN
// line is emitted — neither proves the chain that actually carries the
// guarantee in production (real derived token → requireInternal → handler).
// A regression that reverted sidecarIPCToken to a workspace-bound token, or
// dropped the ctxInternalTokenCrew set in requireInternal, would leave both
// green while silently re-opening the leak.
//
// These tests drive the endpoint through the REAL requireInternal chain with
// REAL derived tokens from a private (non-loopback, Docker-bridge shaped)
// origin — the way a sidecar reaches crewshipd — and assert on the credential
// IDs actually returned, not on log output.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// crewTokenClosureMaster is this file's master internal secret. Every token in
// these tests is derived from it, exactly as the orchestrator derives the
// sidecar's token from cfg.Auth.InternalToken.
const crewTokenClosureMaster = "crew-token-closure-master-0123456789abcdef"

// sidecarOrigin is a Docker-bridge address: private (so it passes
// requireInternal's network gate) but NOT loopback (so the include_values gate
// and the crew-less warning branch both see it as a container caller). This is
// the shape a real per-crew sidecar has when it dials host.docker.internal.
const sidecarOrigin = "172.17.0.5:44321"

type crewClosureIDs struct {
	wsA, crewA, crewB, agentB string
	wsB                       string
}

// seedCrewClosure builds the smallest world in which both the closure and its
// residual are observable: workspace A with two crews (so a sibling-crew
// CREW-scoped credential exists to leak) plus workspace B (so a cross-tenant
// leak would also be visible).
//
// Credentials in ws-A:
//
//	credWsA    — scope=WORKSPACE, no grant rows (the sidecar self-service
//	             create shape; must stay visible to a crew-scoped caller)
//	credCrewA  — scope=CREW, granted to crew-a via credential_crews
//	credCrewB  — scope=CREW, granted to crew-b via agent_credentials
//	             (this is the #1031 leak: crew-a must never see it)
//
// Credential in ws-B:
//
//	credWsB    — scope=WORKSPACE; a ws-A token must never see it
func seedCrewClosure(t *testing.T) (*InternalHandler, crewClosureIDs) {
	t.Helper()
	// Hermetic origin gate: no ALLOW_ANY kill-switch, no trusted proxies, so
	// the network gate and the master-loopback pin behave as in a default
	// deployment regardless of the developer's environment.
	t.Setenv("CREWSHIP_INTERNAL_ALLOW_ANY", "")
	t.Setenv("CREWSHIP_INTERNAL_TRUSTED_PROXIES", "")

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	h := NewInternalHandler(db, crewTokenClosureMaster, newTestLogger())

	ids := crewClosureIDs{
		wsA: "ws_closure_a", crewA: "crew_closure_a", crewB: "crew_closure_b",
		agentB: "agent_closure_b", wsB: "ws_closure_b",
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec: %v\nquery: %s", err, q)
		}
	}
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'A', 'closure-a')`, ids.wsA)
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'closure-b')`, ids.wsB)
	exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'A', 'closure-crew-a')`, ids.crewA, ids.wsA)
	exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'B', 'closure-crew-b')`, ids.crewB, ids.wsA)
	exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES (?, ?, ?, 'AgB', 'closure-ag-b')`,
		ids.agentB, ids.wsA, ids.crewB)

	seedCred := func(credID, wsID, scope string) {
		t.Helper()
		exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider,
			scope, status, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'enc-placeholder', 'AI_CLI_TOKEN', 'ANTHROPIC', ?, 'ACTIVE', ?,
			datetime('now'), datetime('now'))`, credID, wsID, "cred-"+credID, scope, userID)
	}
	seedCred("credWsA", ids.wsA, "WORKSPACE")
	seedCred("credCrewA", ids.wsA, "CREW")
	exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES ('credCrewA', ?)`, ids.crewA)
	seedCred("credCrewB", ids.wsA, "CREW")
	exec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('ac_closure_b', ?, 'credCrewB', 'B', 0, datetime('now'))`, ids.agentB)
	seedCred("credWsB", ids.wsB, "WORKSPACE")

	return h, ids
}

// listThroughRequireInternal drives GET /internal/credentials through the real
// requireInternal middleware with the given X-Internal-Token and origin, and
// returns the HTTP status plus the set of credential IDs in the body. A
// non-200 yields a nil set — callers assert on the status in that case.
func listThroughRequireInternal(t *testing.T, h *InternalHandler, token, origin, query string) (int, map[string]bool) {
	t.Helper()
	target := "/api/v1/internal/credentials"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = origin
	req.Header.Set("X-Internal-Token", token)
	rec := httptest.NewRecorder()
	h.requireInternal(http.HandlerFunc(h.ListCredentials)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var out []struct {
		ID          string  `json:"id"`
		WorkspaceID string  `json:"workspace_id"`
		AccessToken *string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode listing: %v; body=%s", err, rec.Body.String())
	}
	got := map[string]bool{}
	for _, c := range out {
		if c.AccessToken != nil {
			t.Fatalf("metadata listing leaked a plaintext access_token for %s", c.ID)
		}
		got[c.ID] = true
	}
	return rec.Code, got
}

// TestSecCredsCrewToken_OmittedCrewIDCannotWidenListing is the closure the
// #1031 note said was impossible without crew-bound tokens. A per-crew
// sidecar's token (crwv1) pins the crew in the MAC, so a caller that captured
// it inside the container and dropped ?crew_id — the exact fail-open shape —
// still gets a crew-a-scoped listing. crew-b's CREW-scoped credential is
// absent, and so is workspace B's.
//
// This is the test that must go red if the closure regresses. It exercises the
// production chain end to end: DeriveCrewToken → requireInternal MAC check +
// ctxInternalTokenCrew → ListCredentials reading the bound crew.
func TestSecCredsCrewToken_OmittedCrewIDCannotWidenListing(t *testing.T) {
	h, ids := seedCrewClosure(t)
	crewToken := internaltoken.DeriveCrewToken(crewTokenClosureMaster, ids.wsA, ids.crewA)
	if crewToken == "" {
		t.Fatal("DeriveCrewToken returned empty — crew-bound tokens are not being issued")
	}

	// No crew_id, no workspace_id: the caller supplies nothing at all. Both
	// scopes must come from the token.
	code, got := listThroughRequireInternal(t, h, crewToken, sidecarOrigin, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !got["credWsA"] {
		t.Errorf("crew-a must still see the workspace-scoped credential with no grant row (self-service create → immediately invisible would be a functional break), got %v", got)
	}
	if !got["credCrewA"] {
		t.Errorf("crew-a must see its own CREW-scoped credential, got %v", got)
	}
	if got["credCrewB"] {
		t.Errorf("FAIL-OPEN: omitting crew_id enumerated crew-b's CREW-scoped credential — the #1031 leak is back. Check that requireInternal still sets ctxInternalTokenCrew for crwv1 tokens and that ListCredentials still prefers it over ?crew_id. Got %v", got)
	}
	if got["credWsB"] {
		t.Errorf("cross-tenant leak: a ws-A crew token saw workspace B's credential, got %v", got)
	}
}

// TestSecCredsCrewToken_ForgedCrewIDRefused pins the other half of the
// closure: a crew-bound caller that ASKS for a sibling crew is refused at the
// middleware (403), not quietly narrowed or widened. Together with the test
// above, a crew-bound caller can neither omit nor forge the crew scope — which
// is precisely what "crew_id, which any caller can forge" no longer means.
func TestSecCredsCrewToken_ForgedCrewIDRefused(t *testing.T) {
	h, ids := seedCrewClosure(t)
	crewToken := internaltoken.DeriveCrewToken(crewTokenClosureMaster, ids.wsA, ids.crewA)

	code, _ := listThroughRequireInternal(t, h, crewToken, sidecarOrigin, "crew_id="+ids.crewB)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a crew-bound token requesting a sibling crew", code)
	}

	// A forged workspace is refused the same way, so the crew branch can't be
	// used as a cross-tenant read either.
	code, _ = listThroughRequireInternal(t, h, crewToken, sidecarOrigin, "workspace_id="+ids.wsB)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a crew-bound token requesting a foreign workspace", code)
	}
}

// TestSecCredsCrewToken_NoDowngradeToCrewlessView pins that a compromised
// crew container cannot trade its crew-bound token for a crew-less one and so
// reach the workspace-wide listing the residual below permits. The three
// downgrade attempts an in-container attacker actually has:
//
//  1. the per-agent bearer token it legitimately holds in its env (agtv1) —
//     not an internal-API credential at all;
//  2. a crew token with the crew segment rewritten to empty;
//  3. a crew token re-labelled with the workspace-token prefix.
//
// All three must 403. Minting a real wsv1 token requires the master secret,
// which never enters a container.
func TestSecCredsCrewToken_NoDowngradeToCrewlessView(t *testing.T) {
	h, ids := seedCrewClosure(t)

	agentToken := internaltoken.DeriveAgentToken(crewTokenClosureMaster, ids.wsA, ids.agentB)
	if agentToken == "" {
		t.Fatal("DeriveAgentToken returned empty")
	}
	crewMAC := internaltoken.DeriveCrewToken(crewTokenClosureMaster, ids.wsA, ids.crewA)

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"per-agent bearer token", agentToken},
		{"crew token with emptied crew segment", internaltoken.CrewPrefix + "." + ids.wsA + "..deadbeef"},
		{"crew token relabelled as a workspace token", internaltoken.Prefix + "." + ids.wsA + "." + crewMAC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := listThroughRequireInternal(t, h, tc.token, sidecarOrigin, "")
			if code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 — %s must not authorize the internal credential listing", code, tc.name)
			}
		})
	}
}

// TestSecCredsWorkspaceToken_CrewlessListingStaysWorkspaceWide is a SENTINEL,
// not an approval. It documents exactly what the surviving branch of the
// #1031 note returns today, so the residual is pinned by a test instead of
// only by a WARN log.
//
// Reality it pins: a workspace-bound (crwv1-less) caller that omits crew_id
// from a non-loopback origin gets EVERY credential in its bound workspace —
// including a credential scoped to a crew it is not part of. Workspace B stays
// invisible, so the tenant boundary holds; only the crew boundary is absent.
//
// Why this is a residual and not the fail-open the note described:
//
//   - The only tokens that reach this branch are workspace-bound ones, and
//     the ONLY issuer is orchestrator.sidecarIPCToken for a run with no crew
//     (crewID == ""). A crew run's sidecar always gets a crew-bound token and
//     takes the branch pinned above, and it cannot downgrade (see
//     TestSecCredsCrewToken_NoDowngradeToCrewlessView).
//   - A crew-less caller has no crew to be scoped to. Workspace-wide IS its
//     token's true scope, cryptographically injected by requireInternal — not
//     an omission the caller chose.
//   - The master token cannot reach this branch from a container at all: the
//     master-token origin pin refuses any non-loopback master call unless the
//     operator sets CREWSHIP_INTERNAL_ALLOW_ANY=true.
//
// Why it is deliberately NOT tightened to an empty/403 response: this listing
// is what the sidecar's credential reaper reconciles its CredStore against
// (internal/sidecar/credstore_reap.go). A 200 with an empty body makes `keep`
// empty and CredStore.Reap drops EVERY provider key the container booted with
// — a crew-less agent would lose its Anthropic key mid-run. Trading a
// metadata-only listing for an availability break in the credential lifeline
// of running agents is the wrong trade.
//
// If you tighten this branch, this test goes red. That is intended: read the
// two paragraphs above first, and if you still want the tighter behaviour,
// make the reaper distinguish "scoped to nothing" from "nothing is live"
// BEFORE flipping the assertions here.
func TestSecCredsWorkspaceToken_CrewlessListingStaysWorkspaceWide(t *testing.T) {
	h, ids := seedCrewClosure(t)
	wsToken := internaltoken.DeriveWorkspaceToken(crewTokenClosureMaster, ids.wsA)
	if wsToken == "" {
		t.Fatal("DeriveWorkspaceToken returned empty")
	}

	code, got := listThroughRequireInternal(t, h, wsToken, sidecarOrigin, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}

	// The pinned reality, stated positively so a behaviour change of ANY
	// direction trips it rather than only a widening.
	want := map[string]bool{"credWsA": true, "credCrewA": true, "credCrewB": true}
	for id := range want {
		if !got[id] {
			t.Errorf("crew-less workspace-bound listing no longer contains %s — the residual documented above changed; got %v", id, got)
		}
	}
	for id := range got {
		if !want[id] {
			t.Errorf("crew-less workspace-bound listing returned an unexpected credential %s; got %v", id, got)
		}
	}
	// The tenant boundary is NOT part of the residual — it must hold.
	if got["credWsB"] {
		t.Fatalf("cross-tenant leak: a ws-A workspace-bound token saw workspace B's credential, got %v", got)
	}
}

// TestSecCredsWorkspaceToken_CrewlessCallerMayNarrowNotWiden pins that the
// residual above is a ceiling, not a lever. A crew-less caller may still pass
// ?crew_id (the pre-#1159 opt-in, kept for backwards compatibility with older
// sidecars), and doing so can only NARROW the listing it already gets. In
// particular it can never reach another workspace's rows, because
// requireInternal injected the bound workspace_id.
func TestSecCredsWorkspaceToken_CrewlessCallerMayNarrowNotWiden(t *testing.T) {
	h, ids := seedCrewClosure(t)
	wsToken := internaltoken.DeriveWorkspaceToken(crewTokenClosureMaster, ids.wsA)

	_, wide := listThroughRequireInternal(t, h, wsToken, sidecarOrigin, "")
	code, narrow := listThroughRequireInternal(t, h, wsToken, sidecarOrigin, "crew_id="+ids.crewA)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for id := range narrow {
		if !wide[id] {
			t.Errorf("?crew_id widened a crew-less caller's listing with %s — it must only narrow; wide=%v narrow=%v", id, wide, narrow)
		}
	}
	if narrow["credCrewB"] {
		t.Errorf("?crew_id=crew-a still returned crew-b's CREW-scoped credential, got %v", narrow)
	}
}
