package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Owner decision R71 §0.14/1 (overrides §0.3/4): Edit is delegated to an agent
// by the artifact's OWNER only. A workspace ADMIN or OWNER role reads and
// administers ordinary grants, but "read, edit and grant access are different
// capabilities" and the right to delegate Edit to an agent is not implied by a
// workspace role. §0.14/7: Comment is not Propose.
//
// These tests pin both halves of the rule: issuing (PUT /grants) and use time
// (a stored agent write grant whose issuer is not the page owner reaches
// nothing), and they pin what did NOT change: lower agent levels and every
// human grant keep their existing issuers.

func pagesSeedCrewMember(t *testing.T, h *PageHandler, crewID, userID string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`,
		"cm-"+crewID+"-"+userID, crewID, userID); err != nil {
		t.Fatalf("insert crew member %s/%s: %v", crewID, userID, err)
	}
}

func TestAgentEditDelegationIsOwnerOnly(t *testing.T) {
	type call struct {
		name   string
		caller string
		role   string
		body   string
		want   int
	}
	const agentWrite = `{"subject_type":"agent","subject":"scribe","level":"write"}`
	userOwned := []call{
		{"workspace admin may not delegate agent Edit", "admin", "ADMIN", agentWrite, http.StatusForbidden},
		{"another workspace owner may not delegate agent Edit", "wsowner", "OWNER", agentWrite, http.StatusForbidden},
		{"page owner delegates agent Edit", "", "OWNER", agentWrite, http.StatusOK},
		{"admin still grants an agent read", "admin", "ADMIN", `{"subject_type":"agent","subject":"scribe","level":"read"}`, http.StatusOK},
		{"admin may not delegate agent produce either: it edits the data", "admin", "ADMIN", `{"subject_type":"agent","subject":"scribe","level":"produce","panels":["sluzby"]}`, http.StatusForbidden},
		{"admin still grants a person write", "admin", "ADMIN", `{"subject_type":"user","subject":"member@example.com","level":"write"}`, http.StatusOK},
		{"admin still grants the workspace write", "admin", "ADMIN", `{"subject_type":"workspace","level":"write"}`, http.StatusOK},
		{"admin still grants a crew write", "admin", "ADMIN", `{"subject_type":"crew","subject":"engine","level":"write"}`, http.StatusOK},
		{"agent comment stays refused: Comment is not Propose", "", "OWNER", `{"subject_type":"agent","subject":"scribe","level":"comment"}`, http.StatusBadRequest},
		// A person's own Edit grant is not a right to delegate it (§0.3/4).
		{"human Edit grantee may not delegate agent Edit", "editor", "MEMBER", agentWrite, http.StatusForbidden},
		{"human Edit grantee may not grant an agent anything", "editor", "MEMBER", `{"subject_type":"agent","subject":"scribe","level":"read"}`, http.StatusForbidden},
	}
	for _, tc := range userOwned {
		t.Run("user-owned page/"+tc.name, func(t *testing.T) {
			h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
			pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
			pagesSeedUser(t, h, wsID, "wsowner", "wsowner@example.com", "OWNER")
			pagesSeedUser(t, h, wsID, "member", "member@example.com", "MEMBER")
			pagesSeedAgent(t, h, wsID, "agt-scribe", "scribe", "crew-engine")
			pagesSeedUser(t, h, wsID, "editor", "editor@example.com", "MEMBER")
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
				t.Errorf("the refusal must say only the owner delegates Edit; body: %s", rr.Body.String())
			}
		})
	}

	crewOwned := []call{
		{"member of the owning crew delegates agent Edit", "crewmate", "MEMBER", agentWrite, http.StatusOK},
		{"workspace admin outside the crew may not", "admin", "ADMIN", agentWrite, http.StatusForbidden},
	}
	for _, tc := range crewOwned {
		t.Run("crew-owned page/"+tc.name, func(t *testing.T) {
			h, _, wsID, _, _ := pagesGrantFixture(t, "crew/engine")
			pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
			pagesSeedUser(t, h, wsID, "crewmate", "crewmate@example.com", "MEMBER")
			pagesSeedCrewMember(t, h, "crew-engine", "crewmate")
			pagesSeedAgent(t, h, wsID, "agt-scribe", "scribe", "crew-engine")
			rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, tc.caller, tc.role, "fleet-201", tc.body)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// TestAgentEditGrantByNonOwnerIsInertAtUseTime: rows written before the rule
// (or by any other path) do not keep working. An agent write grant whose
// issuer is not the page owner now reaches nothing — not the spec, not the
// proposal path — and the listing shows it inert with the reason.
func TestAgentEditGrantByNonOwnerIsInertAtUseTime(t *testing.T) {
	h, _, wsID, ownerID, pageID := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
	pagesSeedAgent(t, h, wsID, "agt-admins", "admins-pick", "crew-engine")
	pagesSeedAgent(t, h, wsID, "agt-owners", "owners-pick", "crew-engine")
	if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id, granted_at)
		VALUES (?, 'agent', 'agt-admins', 'write', 'admin', '2026-11-01T00:00:00Z')`, pageID); err != nil {
		t.Fatalf("seed admin-issued agent write: %v", err)
	}
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"owners-pick","level":"write"}`)

	ctx := context.Background()
	rec, err := h.loadPage(ctx, wsID, "fleet-201")
	if err != nil {
		t.Fatalf("load page: %v", err)
	}
	for _, tc := range []struct {
		agent string
		want  bool
	}{
		{"agt-admins", false},
		{"agt-owners", true},
	} {
		if got := h.agentMayEditSpec(ctx, wsID, rec, tc.agent); got != tc.want {
			t.Errorf("agentMayEditSpec(%s) = %v, want %v", tc.agent, got, tc.want)
		}
		if got := h.agentMayProposeOn(ctx, wsID, rec, tc.agent); got != tc.want {
			t.Errorf("agentMayProposeOn(%s) = %v, want %v", tc.agent, got, tc.want)
		}
		if got := collaborationAgentGrantAllows(ctx, h, wsID, rec, tc.agent, true); got != tc.want {
			t.Errorf("collaborationAgentGrantAllows(%s, write) = %v, want %v", tc.agent, got, tc.want)
		}
	}

	for _, g := range pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201") {
		if g["subject_id"] != "agt-admins" {
			continue
		}
		if live, _ := g["live"].(bool); live {
			t.Errorf("admin-issued agent write is listed live: %+v", g)
		}
		if reason, _ := g["inert_reason"].(string); !strings.Contains(reason, "owner") {
			t.Errorf("inert_reason = %q, want it to say only the owner delegates Edit", reason)
		}
	}

	// An admin re-issuing the owner's agent Edit grant would re-anchor it to
	// the admin; that is a delegation too, and it is refused.
	rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, "admin", "ADMIN", "fleet-201",
		`{"subject_type":"agent","subject":"owners-pick","level":"write"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("admin re-issue: status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	if !h.agentMayEditSpec(ctx, wsID, rec, "agt-owners") {
		t.Error("the refused re-issue changed the owner's grant")
	}

	// Revoking narrows access, and an admin may still do it.
	rr = pagesGrantCall(t, h, "DELETE", "/api/v1/pages/fleet-201/grants?subject_type=agent&subject=owners-pick&level=write", wsID, "admin", "ADMIN", "fleet-201", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("admin revoke: status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestAgentCommentOrReadNeverProposes — §0.14/7: Comment does not authorise a
// proposal. An agent cannot hold comment at all, and a read grant is not a
// proposer.
func TestAgentCommentOrReadNeverProposes(t *testing.T) {
	h, _, wsID, ownerID, pageID := pagesGrantFixture(t, "")
	pagesSeedAgent(t, h, wsID, "agt-reader", "reader", "crew-engine")
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"reader","level":"read"}`)
	if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id, granted_at)
		VALUES (?, 'agent', 'agt-reader', 'comment', ?, '2026-11-01T00:00:00Z')`, pageID, ownerID); err == nil {
		t.Error("the schema accepted a comment grant for an agent")
	}
	rec, err := h.loadPage(context.Background(), wsID, "fleet-201")
	if err != nil {
		t.Fatalf("load page: %v", err)
	}
	if h.agentMayProposeOn(context.Background(), wsID, rec, "agt-reader") {
		t.Error("a read grant made the agent a proposer")
	}
}

// TestGrantListingSaysWhoMayDelegateAgentEdit: the client hides Edit-for-agent
// from everyone the server would refuse, so it needs the server's answer.
func TestGrantListingSaysWhoMayDelegateAgentEdit(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
	for _, tc := range []struct {
		caller, role string
		want         bool
	}{
		{ownerID, "OWNER", true},
		{"admin", "ADMIN", false},
	} {
		rr := pagesGrantCall(t, h, "GET", "/api/v1/pages/fleet-201/grants", wsID, tc.caller, tc.role, "fleet-201", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("list as %s: %d %s", tc.caller, rr.Code, rr.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got, ok := out["can_delegate_agent_edit"].(bool); !ok || got != tc.want {
			t.Errorf("can_delegate_agent_edit for %s = %v (present %v), want %v", tc.caller, out["can_delegate_agent_edit"], ok, tc.want)
		}
	}
}

// The use-time rule on a crew-owned page: a member of the owning crew vouches
// for an agent write grant, a workspace admin outside it does not, and the
// owner's grant goes inert when its issuer leaves the owning crew.
func TestAgentEditGrantOnCrewPageFollowsCrewOwnership(t *testing.T) {
	h, _, wsID, _, pageID := pagesGrantFixture(t, "crew/engine")
	pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
	pagesSeedUser(t, h, wsID, "crewmate", "crewmate@example.com", "ADMIN")
	pagesSeedCrewMember(t, h, "crew-engine", "crewmate")
	pagesSeedAgent(t, h, wsID, "agt-a", "by-admin", "crew-engine")
	pagesSeedAgent(t, h, wsID, "agt-c", "by-crewmate", "crew-engine")
	for agent, issuer := range map[string]string{"agt-a": "admin", "agt-c": "crewmate"} {
		if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id, granted_at)
			VALUES (?, 'agent', ?, 'write', ?, '2026-11-01T00:00:00Z')`, pageID, agent, issuer); err != nil {
			t.Fatalf("seed %s: %v", agent, err)
		}
	}
	ctx := context.Background()
	rec, err := h.loadPage(ctx, wsID, "fleet-201")
	if err != nil {
		t.Fatalf("load page: %v", err)
	}
	if h.agentMayProposeOn(ctx, wsID, rec, "agt-a") {
		t.Error("an admin outside the owning crew vouched for agent Edit")
	}
	if !h.agentMayProposeOn(ctx, wsID, rec, "agt-c") {
		t.Fatal("a member of the owning crew could not delegate agent Edit")
	}
	// crewmate stays a workspace ADMIN, so the issuer-standing view keeps the
	// row; only ownership decides agent Edit.
	if _, err := h.db.Exec(`DELETE FROM crew_members WHERE crew_id = 'crew-engine' AND user_id = 'crewmate'`); err != nil {
		t.Fatalf("leave crew: %v", err)
	}
	if h.agentMayProposeOn(ctx, wsID, rec, "agt-c") {
		t.Error("agent Edit outlived its issuer's ownership of the page")
	}
}
