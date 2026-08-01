package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1657 — a credential named `github-token` in the UI killed every run of its
// crew. The crew-link delivery arm resolves the slot to credentials.name, a
// human identity for an account that nothing held to the env-var charset, and
// the orchestrator's file writer treated the mismatch as fatal for the WHOLE
// batch.
//
// These tests pin the API half: the name is normalised onto the variable the
// operator obviously meant, exactly once, with the agent's whole delivery set
// in view — and a normalised name never takes a variable from a credential that
// asked for it by name.

// TestCrewDelivery_DisplayNameIsNormalisedToAVariable is the headline. It runs
// both delivery paths because a normalisation that lands in one resolver and
// not the other is indistinguishable from the bug it replaces.
func TestCrewDelivery_DisplayNameIsNormalisedToAVariable(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-gh", "github-token", "ghp_x")
	// The other spelling that is legal to write and illegal to read. It needs
	// no dash — lowercase alone fails the reader — so it is the case a fold
	// that only handled dashes would miss.
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-npm", "npm_token", "npm_y")
	linkCredToCrew(t, db, "cd-gh", e.crewA)
	linkCredToCrew(t, db, "cd-npm", e.crewA)

	t.Run("boot", func(t *testing.T) {
		got := bootEnvValues(bootCreds(t, db, e.agentA))
		if got["GITHUB_TOKEN"] != "ghp_x" {
			t.Fatalf("boot payload = %v, want GITHUB_TOKEN=ghp_x — a credential named "+
				"github-token must reach the container as the variable it meant", got)
		}
		if got["NPM_TOKEN"] != "npm_y" {
			t.Fatalf("boot payload = %v, want NPM_TOKEN=npm_y — a lowercase name is just as "+
				"unreadable to the container as a dashed one", got)
		}
		for _, raw := range []string{"github-token", "npm_token"} {
			if _, present := got[raw]; present {
				t.Errorf("boot payload still carries the raw display name %q: %v", raw, got)
			}
		}
	})
	t.Run("delegation", func(t *testing.T) {
		got := delegationEnvValues(delegationCreds(t, db, e.agentA))
		if got["GITHUB_TOKEN"] != "ghp_x" || got["NPM_TOKEN"] != "npm_y" {
			t.Fatalf("delegation payload = %v, want GITHUB_TOKEN=ghp_x and NPM_TOKEN=npm_y", got)
		}
	})
}

// TestCrewDelivery_NormalisedNameNeverTakesAnExactMatchsVariable is the
// collision rule, and the reason normalising is safe to do at all.
//
// `GITHUB_TOKEN` and `github-token` are two legal, distinct credential names in
// one workspace — credentials.name is UNIQUE per workspace and both spellings
// pass it — and they fold onto the same variable. The credential that asked for
// GITHUB_TOKEN **by name** keeps it; the folded one is dropped and reported.
//
// The fixture stacks the deck against that rule on purpose. The folding
// candidate is an explicit per-agent grant (source rank 0) and the exact-named
// one is a crew link (rank 4), so the folding candidate is FIRST in delivery
// order and would win outright if the resolution were a single pass in row
// order. It must still lose: the name it wants is our invention, and the other
// credential's actual name. An agent pushing as the wrong bot is not something
// a rename we made up should be able to cause.
//
// (The grant's env_var_name is written straight to the table because the assign
// endpoint no longer accepts that spelling — this is the legacy row every
// existing workspace may be holding.)
func TestCrewDelivery_NormalisedNameNeverTakesAnExactMatchsVariable(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-fold", "some-account", "wrong-bot")
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-exact", "GITHUB_TOKEN", "right-bot")
	assignCredToAgent(t, db, "cd-fold", e.agentA, "github-token", 0)
	linkCredToCrew(t, db, "cd-exact", e.crewA)

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if got["GITHUB_TOKEN"] != "right-bot" {
		t.Fatalf("GITHUB_TOKEN = %q, want \"right-bot\" — a credential we RENAMED took the "+
			"variable from the one that asked for it by name; the agent now acts as the wrong account", got["GITHUB_TOKEN"])
	}
	// The loser is undelivered rather than delivered under a made-up second
	// name: two variables holding two GitHub tokens is not what anyone asked
	// for, and the container has one default identity per tool.
	for env, val := range got {
		if val == "wrong-bot" {
			t.Errorf("the folded credential was delivered anyway, as %q", env)
		}
	}
}

// TestResolveDeliverySlots_ExactNameWinsRegardlessOfOrder is the two-pass rule
// stated without a database, because the rule is precisely about ORDER and the
// delivery query's order is not something a fixture can pin reliably.
//
// Both orderings must produce the same answer. A single pass in row order gives
// GH_TOKEN to whichever row it met first, which for one of the two orderings is
// the credential we RENAMED — and the agent then acts as an account nobody
// selected, with the credential that is actually called GH_TOKEN silently gone.
func TestResolveDeliverySlots_ExactNameWinsRegardlessOfOrder(t *testing.T) {
	t.Parallel()
	fold := deliveredCredential{ID: "fold", EnvVar: "gh-token"}
	exact := deliveredCredential{ID: "exact", EnvVar: "GH_TOKEN"}

	for _, order := range [][]deliveredCredential{{fold, exact}, {exact, fold}} {
		in := append([]deliveredCredential(nil), order...)
		kept, notices := resolveDeliverySlots(in)
		if len(kept) != 1 || kept[0].ID != "exact" || kept[0].EnvVar != "GH_TOKEN" {
			t.Fatalf("order %s: kept = %+v, want only the credential named GH_TOKEN, under GH_TOKEN",
				order[0].ID+"-first", kept)
		}
		if len(notices) != 1 || notices[0].CredentialID != "fold" || notices[0].Delivered != "" {
			t.Fatalf("order %s: notices = %+v, want the folded credential reported as undelivered",
				order[0].ID+"-first", notices)
		}
		if !strings.Contains(notices[0].Reason, "exact") {
			t.Errorf("order %s: the notice must name the credential holding the variable, got %q",
				order[0].ID+"-first", notices[0].Reason)
		}
	}
}

// TestCrewDelivery_UnnameableCredentialCostsOnlyItself covers the name no fold
// can rescue. It must not be delivered — and it must not take its neighbours
// with it, which is the property the whole issue is about.
func TestCrewDelivery_UnnameableCredentialCostsOnlyItself(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-bad", "my token!", "unreachable")
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-good", "DEPLOY_KEY", "keymaterial")
	linkCredToCrew(t, db, "cd-bad", e.crewA)
	linkCredToCrew(t, db, "cd-good", e.crewA)

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if got["DEPLOY_KEY"] != "keymaterial" {
		t.Fatalf("boot payload = %v, want DEPLOY_KEY=keymaterial — the well-named "+
			"credential must survive its badly-named neighbour", got)
	}
	for env, val := range got {
		if val == "unreachable" {
			t.Errorf("a credential named %q was delivered as %q", "my token!", env)
		}
	}
}

// TestCrewDelivery_AlreadyLegalNamesAreUntouched is the no-op guarantee. Every
// existing workspace is in this state, and a normalisation pass that reordered
// or rewrote anything here would be a migration nobody asked for.
func TestCrewDelivery_AlreadyLegalNamesAreUntouched(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-1", "CREW_TOKEN", "v1")
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-2", "_PRIVATE", "v2")
	linkCredToCrew(t, db, "cd-1", e.crewA)
	linkCredToCrew(t, db, "cd-2", e.crewA)

	got := bootEnvValues(bootCreds(t, db, e.agentA))
	if got["CREW_TOKEN"] != "v1" || got["_PRIVATE"] != "v2" {
		t.Fatalf("boot payload = %v, want both names delivered verbatim", got)
	}
	for _, c := range bootCreds(t, db, e.agentA) {
		if c.EnvVar != "CREW_TOKEN" && c.EnvVar != "_PRIVATE" {
			t.Errorf("unexpected slot %q — a legal name was rewritten", c.EnvVar)
		}
	}
}

// TestCrewDelivery_FieldsHangOffTheNormalisedSlot pins the ORDER of the two
// passes. A part's name is derived from its credential's slot, so normalising
// after attaching parts would derive github-token_REGION — which is not a legal
// variable, so the part would be dropped — while the credential itself arrived
// perfectly well as GITHUB_TOKEN. The part must follow its credential.
func TestCrewDelivery_FieldsHangOffTheNormalisedSlot(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-aws", "aws-prod", "akid")
	seedCredentialField(t, db, "cd-aws", "region", "eu-central-1", false, 0)
	linkCredToCrew(t, db, "cd-aws", e.crewA)

	var found bool
	for _, c := range bootCreds(t, db, e.agentA) {
		for _, f := range c.Fields {
			if f.EnvVar == "AWS_PROD_REGION" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no AWS_PROD_REGION in the boot payload — the part was named off the "+
			"raw display name instead of the slot its credential is delivered under: %+v",
			bootCreds(t, db, e.agentA))
	}
}

// TestResolveForAgent_WarnsAboutRenamedAndDroppedCredentials is where an
// operator can actually READ any of this. #1641 put warnings on the crew
// endpoints as an additive array; this is the same shape on the endpoint that
// answers "which credential fills which variable, and why", which is the
// question a renamed or dropped credential is an answer to.
func TestResolveForAgent_WarnsAboutRenamedAndDroppedCredentials(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-fold", "github-token", "v1")
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-bad", "my token!", "v2")
	linkCredToCrew(t, db, "cd-fold", e.crewA)
	linkCredToCrew(t, db, "cd-bad", e.crewA)

	h := NewCredentialBindingHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentA+"/credential-bindings", nil)
	req.SetPathValue("agentId", e.agentA)
	req = req.WithContext(withWorkspace(req.Context(), e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveForAgent(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}

	var got struct {
		Slots []struct {
			Slot           string `json:"slot"`
			CredentialName string `json:"credential_name"`
		} `json:"slots"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	joined := strings.Join(got.Warnings, "\n")
	if !strings.Contains(joined, "github-token") || !strings.Contains(joined, "GITHUB_TOKEN") {
		t.Errorf("warnings must name what the operator typed AND what the agent gets, got:\n%s", joined)
	}
	if !strings.Contains(joined, "my token!") || !strings.Contains(joined, "NOT delivered") {
		t.Errorf("warnings must say the unnameable credential is not delivered, got:\n%s", joined)
	}
	// The undeliverable one is a warning, not a slot: an empty SLOT column would
	// read as "delivered under no name", the exact ambiguity this view removes.
	for _, s := range got.Slots {
		if s.Slot == "" {
			t.Errorf("an empty slot was listed in the slot map: %+v", got.Slots)
		}
	}
	if len(got.Slots) != 1 || got.Slots[0].Slot != "GITHUB_TOKEN" {
		t.Errorf("slots = %+v, want the one normalised slot", got.Slots)
	}
}

// TestResolveForAgent_NoWarningsKeyWhenEveryNameIsLegal keeps the wire shape
// additive: the key must vanish rather than appear as an empty array, so a
// client that has never seen it decodes an unchanged response.
func TestResolveForAgent_NoWarningsKeyWhenEveryNameIsLegal(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-ok", "CREW_TOKEN", "v1")
	linkCredToCrew(t, db, "cd-ok", e.crewA)

	h := NewCredentialBindingHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/"+e.agentA+"/credential-bindings", nil)
	req.SetPathValue("agentId", e.agentA)
	req = req.WithContext(withWorkspace(req.Context(), e.wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.ResolveForAgent(rr, req)

	if strings.Contains(rr.Body.String(), "warnings") {
		t.Errorf("a warnings key appeared for an agent whose names are all legal: %s", rr.Body.String())
	}
}
