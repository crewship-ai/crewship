package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// R71 §0.14/1 covers an artifact's data as well as its spec: `produce` pushes
// payloads into a dashboard's panels, which is Edit on the artifact's data, so
// only the owner delegates it to an agent. A workspace ADMIN keeps revoke.
func TestAgentProduceDelegationIsOwnerOnly(t *testing.T) {
	const agentProduce = `{"subject_type":"agent","subject":"scribe","level":"produce","panels":["sluzby"]}`
	for _, tc := range []struct {
		name, caller, role, body string
		want                     int
	}{
		{"page owner delegates agent produce", "", "OWNER", agentProduce, http.StatusOK},
		{"page owner delegates whole-page agent produce", "", "OWNER", `{"subject_type":"agent","subject":"scribe","level":"produce"}`, http.StatusOK},
		{"workspace admin may not delegate agent produce", "admin", "ADMIN", agentProduce, http.StatusForbidden},
		{"another workspace owner may not delegate agent produce", "wsowner", "OWNER", agentProduce, http.StatusForbidden},
		{"human Edit grantee may not delegate agent produce", "editor", "MEMBER", agentProduce, http.StatusForbidden},
		{"admin still grants an agent read", "admin", "ADMIN", `{"subject_type":"agent","subject":"scribe","level":"read"}`, http.StatusOK},
		{"admin still grants a crew produce", "admin", "ADMIN", `{"subject_type":"crew","subject":"engine","level":"produce"}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
			pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
			pagesSeedUser(t, h, wsID, "wsowner", "wsowner@example.com", "OWNER")
			pagesSeedUser(t, h, wsID, "editor", "editor@example.com", "MEMBER")
			pagesSeedAgent(t, h, wsID, "agt-scribe", "scribe", "crew-engine")
			pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"editor@example.com","level":"write"}`)
			caller := tc.caller
			if caller == "" {
				caller = ownerID
			}
			rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, caller, tc.role, "fleet-201", tc.body)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", rr.Code, tc.want, rr.Body.String())
			}
			if tc.want == http.StatusForbidden && !strings.Contains(rr.Body.String(), "owner") {
				t.Errorf("the refusal must say only the owner delegates; body: %s", rr.Body.String())
			}
		})
	}

	t.Run("admin revokes the owner's agent produce", func(t *testing.T) {
		h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
		pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
		pagesSeedAgent(t, h, wsID, "agt-scribe", "scribe", "crew-engine")
		pagesGrant(t, h, wsID, ownerID, "fleet-201", agentProduce)
		rr := pagesGrantCall(t, h, "DELETE", "/api/v1/pages/fleet-201/grants?subject_type=agent&subject=scribe&level=produce", wsID, "admin", "ADMIN", "fleet-201", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("admin revoke: status = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
	})
}

func pagesAgentGrantRetired(t *testing.T, h *PageHandler, pageID, agentID, level string) bool {
	t.Helper()
	var retired bool
	if err := h.db.QueryRow(`SELECT retired_at IS NOT NULL FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id=? AND level=?`,
		pageID, agentID, level).Scan(&retired); err != nil {
		t.Fatalf("read agent %s %s grant: %v", agentID, level, err)
	}
	return retired
}

func pagesDelegationRetirementAudits(t *testing.T, h *PageHandler, pageID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='page.agent_grant.retired_not_owner' AND entity_type='page' AND entity_id=?`, pageID).Scan(&n); err != nil {
		t.Fatalf("count retirement audits: %v", err)
	}
	return n
}

// A non-owner-issued agent write or produce row is judged when it is written
// and retired for good, with an audit entry. Its issuer later owning the page
// does not revive it; only a new grant by the then-owner does.
func TestNonOwnerAgentDelegationIsRetiredForGood(t *testing.T) {
	h, _, wsID, ownerID, pageID := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
	pagesSeedAgent(t, h, wsID, "agt-w", "writer", "crew-engine")
	pagesSeedAgent(t, h, wsID, "agt-p", "producer", "crew-engine")
	pagesSeedAgent(t, h, wsID, "agt-r", "reader", "crew-engine")
	// Rows written by any path but the owner-checked PUT: a restore, an older
	// build, a direct write.
	for _, g := range [][2]string{{"agt-w", "write"}, {"agt-p", "produce"}, {"agt-r", "read"}} {
		if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id, granted_at)
			VALUES (?, 'agent', ?, ?, 'admin', '2026-11-01T00:00:00Z')`, pageID, g[0], g[1]); err != nil {
			t.Fatalf("seed admin-issued agent %s: %v", g[1], err)
		}
	}
	if !pagesAgentGrantRetired(t, h, pageID, "agt-w", "write") || !pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
		t.Fatal("a non-owner-issued agent write/produce row was stored live")
	}
	if pagesAgentGrantRetired(t, h, pageID, "agt-r", "read") {
		t.Fatal("an admin-issued agent read grant was retired; only write and produce are owner-only")
	}
	if n := pagesDelegationRetirementAudits(t, h, pageID); n != 2 {
		t.Fatalf("retirement audits = %d, want 2", n)
	}

	ctx := context.Background()
	assertInert := func(stage string) {
		t.Helper()
		rec, err := h.loadPage(ctx, wsID, "fleet-201")
		if err != nil {
			t.Fatalf("load page: %v", err)
		}
		if h.agentMayEditSpec(ctx, wsID, rec, "agt-w") {
			t.Errorf("%s: the non-owner-issued agent write grant edits the spec", stage)
		}
		if h.agentMayProduce(ctx, wsID, rec, "agt-p", "sluzby") {
			t.Errorf("%s: the non-owner-issued agent produce grant pushes data", stage)
		}
	}
	assertInert("as stored")
	for _, g := range pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201") {
		if id := g["subject_id"]; id != "agt-w" && id != "agt-p" {
			continue
		}
		if live, _ := g["live"].(bool); live {
			t.Errorf("listed live: %+v", g)
		}
		if reason, _ := g["inert_reason"].(string); !strings.Contains(reason, "owner") || !strings.Contains(reason, "new grant") {
			t.Errorf("inert_reason = %q, want it to say only the owner delegates and a new grant is needed", reason)
		}
	}

	// The issuer becomes the owner. Nothing the owner did vouches for the old rows.
	if _, err := h.db.Exec(`UPDATE pages SET owner_user_id='admin' WHERE id=?`, pageID); err != nil {
		t.Fatalf("transfer ownership: %v", err)
	}
	assertInert("after the issuer became owner")
	if !pagesAgentGrantRetired(t, h, pageID, "agt-w", "write") || !pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
		t.Fatal("ownership transfer revived a retired agent delegation")
	}
	for _, g := range pagesGrantList(t, h, wsID, "admin", "ADMIN", "fleet-201") {
		if id := g["subject_id"]; id != "agt-w" && id != "agt-p" {
			continue
		}
		if reason, _ := g["inert_reason"].(string); !strings.Contains(reason, "new grant") {
			t.Errorf("after transfer inert_reason = %q, want the delegation reason kept", reason)
		}
	}

	// Re-delegation is an explicit new grant by the then-owner.
	pagesGrant(t, h, wsID, "admin", "fleet-201", `{"subject_type":"agent","subject":"writer","level":"write"}`)
	pagesGrant(t, h, wsID, "admin", "fleet-201", `{"subject_type":"agent","subject":"producer","level":"produce"}`)
	rec, err := h.loadPage(ctx, wsID, "fleet-201")
	if err != nil {
		t.Fatalf("load page: %v", err)
	}
	if !h.agentMayEditSpec(ctx, wsID, rec, "agt-w") || !h.agentMayProduce(ctx, wsID, rec, "agt-p", "sluzby") {
		t.Fatal("the new owner's explicit grant did not delegate")
	}
}

// An owner-issued delegation ends when its issuer stops owning the page, and
// stays ended if ownership comes back.
func TestAgentDelegationEndsWithItsIssuersOwnership(t *testing.T) {
	t.Run("user-owned page transferred away and back", func(t *testing.T) {
		h, _, wsID, ownerID, pageID := pagesGrantFixture(t, "")
		pagesSeedUser(t, h, wsID, "heir", "heir@example.com", "MEMBER")
		pagesSeedAgent(t, h, wsID, "agt-w", "writer", "crew-engine")
		pagesSeedAgent(t, h, wsID, "agt-p", "producer", "crew-engine")
		pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"writer","level":"write"}`)
		pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"producer","level":"produce"}`)
		if pagesAgentGrantRetired(t, h, pageID, "agt-w", "write") || pagesDelegationRetirementAudits(t, h, pageID) != 0 {
			t.Fatal("the owner's own delegation was retired")
		}
		if _, err := h.db.Exec(`UPDATE pages SET owner_user_id='heir' WHERE id=?`, pageID); err != nil {
			t.Fatalf("transfer: %v", err)
		}
		if !pagesAgentGrantRetired(t, h, pageID, "agt-w", "write") || !pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
			t.Fatal("the previous owner's agent delegation survived the transfer")
		}
		if n := pagesDelegationRetirementAudits(t, h, pageID); n != 2 {
			t.Fatalf("retirement audits = %d, want 2", n)
		}
		if _, err := h.db.Exec(`UPDATE pages SET owner_user_id=? WHERE id=?`, ownerID, pageID); err != nil {
			t.Fatalf("transfer back: %v", err)
		}
		if !pagesAgentGrantRetired(t, h, pageID, "agt-w", "write") {
			t.Fatal("transferring ownership back revived the delegation")
		}
	})

	t.Run("crew-owned page: issuer leaves the crew and rejoins", func(t *testing.T) {
		h, _, wsID, _, pageID := pagesGrantFixture(t, "crew/engine")
		pagesSeedUser(t, h, wsID, "crewmate", "crewmate@example.com", "ADMIN")
		pagesSeedCrewMember(t, h, "crew-engine", "crewmate")
		pagesSeedAgent(t, h, wsID, "agt-p", "producer", "crew-engine")
		pagesGrant(t, h, wsID, "crewmate", "fleet-201", `{"subject_type":"agent","subject":"producer","level":"produce"}`)
		if pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
			t.Fatal("a member of the owning crew could not delegate produce")
		}
		if _, err := h.db.Exec(`DELETE FROM crew_members WHERE crew_id='crew-engine' AND user_id='crewmate'`); err != nil {
			t.Fatalf("leave crew: %v", err)
		}
		if !pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
			t.Fatal("the delegation outlived its issuer's membership of the owning crew")
		}
		pagesSeedCrewMember(t, h, "crew-engine", "crewmate")
		if !pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
			t.Fatal("rejoining the crew revived the delegation")
		}
		if n := pagesDelegationRetirementAudits(t, h, pageID); n != 1 {
			t.Fatalf("retirement audits = %d, want 1", n)
		}
	})
}

// A deleted crew owns nothing it can delegate: its members no longer count as
// the page's owner for agent delegation or for lending their write to an agent
// run, the delegations they made end for good with an audit entry, and
// restoring the crew does not revive them.
func TestDeletedOwningCrewDelegatesNothing(t *testing.T) {
	h, _, wsID, _, pageID := pagesGrantFixture(t, "crew/engine")
	pagesSeedUser(t, h, wsID, "crewmate", "crewmate@example.com", "MEMBER")
	pagesSeedCrewMember(t, h, "crew-engine", "crewmate")
	pagesSeedAgent(t, h, wsID, "agt-w", "writer", "crew-engine")
	pagesSeedAgent(t, h, wsID, "agt-p", "producer", "crew-engine")
	pagesGrant(t, h, wsID, "crewmate", "fleet-201", `{"subject_type":"agent","subject":"writer","level":"write"}`)
	pagesGrant(t, h, wsID, "crewmate", "fleet-201", `{"subject_type":"agent","subject":"producer","level":"produce"}`)
	ctx := context.Background()
	if !h.humanAgentMayWrite(ctx, wsID, pageID, "crewmate", "agt-other") {
		t.Fatal("a member of the live owning crew could not lend an agent run its write")
	}

	if _, err := h.db.Exec(`UPDATE crews SET deleted_at='2026-11-09T00:00:00Z' WHERE id='crew-engine'`); err != nil {
		t.Fatalf("delete crew: %v", err)
	}
	if !pagesAgentGrantRetired(t, h, pageID, "agt-w", "write") || !pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
		t.Fatal("delegations made by the deleted crew's member survived the deletion")
	}
	if n := pagesDelegationRetirementAudits(t, h, pageID); n != 2 {
		t.Fatalf("retirement audits = %d, want 2", n)
	}
	rec, err := h.loadPage(ctx, wsID, "fleet-201")
	if err != nil {
		t.Fatalf("load page: %v", err)
	}
	if h.agentMayEditSpec(ctx, wsID, rec, "agt-w") || h.agentMayProduce(ctx, wsID, rec, "agt-p", "sluzby") {
		t.Fatal("an agent still acts on a delegation from a deleted crew")
	}
	if h.mayDelegateAgentEdit(ctx, wsID, "crewmate", rec) {
		t.Error("a member of a deleted crew may still delegate agent Edit")
	}
	if h.humanAgentMayWrite(ctx, wsID, pageID, "crewmate", "agt-other") {
		t.Error("a member of a deleted crew still lends an agent run its write")
	}
	for _, level := range []string{"write", "produce"} {
		rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, "crewmate", "MEMBER", "fleet-201",
			`{"subject_type":"agent","subject":"writer","level":"`+level+`"}`)
		if rr.Code == http.StatusOK {
			t.Errorf("a member of a deleted crew delegated agent %s", level)
		}
	}

	if _, err := h.db.Exec(`UPDATE crews SET deleted_at=NULL WHERE id='crew-engine'`); err != nil {
		t.Fatalf("restore crew: %v", err)
	}
	if !pagesAgentGrantRetired(t, h, pageID, "agt-w", "write") || !pagesAgentGrantRetired(t, h, pageID, "agt-p", "produce") {
		t.Fatal("restoring the crew revived its member's delegations")
	}
}
