package api

// Pages — the grants surface (docs/prd/pages.md §7.1 rules 3–5, §7.1b, §11).
//
// Six rules are enforced by pages_grants.go / pages_grants_authz.go, and each
// one has a test below, in order:
//
//	1. Only a human issues a grant (§7.1b rule 1).
//	2. An agent's authority is a subset of the granting human's, evaluated at
//	   USE time (§7.1b) — the load-bearing one, and the one a periodic sweep
//	   would only approximate.
//	3. A grant widens access to the page, never to a crew's data (§7.1 rule 3).
//	4. A produce grant scoped to panel A cannot write panel B (§7.1b).
//	5. Every grant change is journalled (§7.1b).
//	6. `write` is authority over arrangement, never over content (§7.1b rule 2).
//
// Plus the two that make the surface usable rather than merely safe: who may
// administer the ACL at all, and that revoke is symmetric with grant (§11b
// decision 13 — "an asymmetric revoke is how a grant becomes impossible to
// remove").

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// ── Fixture ────────────────────────────────────────────────────────────────

// pagesTwoPanelBody is a page whose two panels are owned by DIFFERENT crews.
// Every rule below is about one caller's standing differing per panel, and a
// single-crew page cannot express that.
func pagesTwoPanelBody(slug, owner string) string {
	ownerLine := ""
	if owner != "" {
		ownerLine = `"owner": "` + owner + `",`
	}
	return `{
		"slug": "` + slug + `",
		"name": "Flotila .201",
		` + ownerLine + `
		"panels": [
			{"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
			 "owner": "crew/lookout", "producer": "script/watch-services.sh", "sla_seconds": 30, "span": 8},
			{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
			 "owner": "crew/engine", "producer": "script/load.sh", "sla_seconds": 60, "span": 4}
		]
	}`
}

const pagesMetricPayload = `{"value":42}`

// pagesGrantFixture is newPagesFixture plus the second crew, the two-panel
// page, and whatever ownership the test needs.
func pagesGrantFixture(t *testing.T, owner string) (*PageHandler, *pagesJournalSpy, string, string, string) {
	t.Helper()
	h, spy, _, wsID, userID := newPagesFixture(t)
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-engine', ?, 'Engine', 'engine')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	req := pagesGrantRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesTwoPanelBody("fleet-201", owner))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page: status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	pageID, _ := doc["id"].(string)
	return h, spy, wsID, userID, pageID
}

// pagesGrantRequest builds a HUMAN-authenticated request.
//
// The auth kind is what makes it human: RequireAuth is the one place that
// records which credential authenticated a request, and it records one for
// both credentials a person presents — an interactive session and a CLI token
// (middleware.go). The grants handler refuses a request carrying neither
// (§7.1b rule 1), so a test that omitted this would be testing the refusal,
// not the feature.
func pagesGrantRequest(t *testing.T, method, target, wsID, userID, role, body string) *http.Request {
	t.Helper()
	req := pagesRequest(t, method, target, wsID, userID, role, body)
	return req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindCLIToken))
}

// pagesGrantCall dispatches one grants request and returns the recorder.
func pagesGrantCall(t *testing.T, h *PageHandler, method, target, wsID, userID, role, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesGrantRequest(t, method, target, wsID, userID, role, body)
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	switch method {
	case "GET":
		h.ListGrants(rr, req)
	case "PUT":
		h.PutGrant(rr, req)
	case "DELETE":
		h.DeleteGrant(rr, req)
	default:
		t.Fatalf("pagesGrantCall: unsupported method %q", method)
	}
	return rr
}

// pagesGrant issues one grant as the workspace OWNER and fails the test if it
// was refused.
func pagesGrant(t *testing.T, h *PageHandler, wsID, userID, slug, body string) {
	t.Helper()
	rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/"+slug+"/grants", wsID, userID, "OWNER", slug, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("grant: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
}

// pagesGrantList reads the ACL back.
func pagesGrantList(t *testing.T, h *PageHandler, wsID, userID, role, slug string) []map[string]any {
	t.Helper()
	rr := pagesGrantCall(t, h, "GET", "/api/v1/pages/"+slug+"/grants", wsID, userID, role, slug, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list grants: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Grants []map[string]any `json:"grants"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("grants response is not JSON: %v — %s", err, rr.Body.String())
	}
	return out.Grants
}

// pagesSeedUser adds a workspace member.
func pagesSeedUser(t *testing.T, h *PageHandler, wsID, id, email, role string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, id, email, id); err != nil {
		t.Fatalf("insert user %s: %v", id, err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`,
		"wm-"+id, wsID, id, role); err != nil {
		t.Fatalf("insert membership %s: %v", id, err)
	}
}

// pagesSeedAgent adds an agent to a crew.
func pagesSeedAgent(t *testing.T, h *PageHandler, wsID, id, slug, crewID string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, ?, ?)`,
		id, crewID, wsID, slug, slug); err != nil {
		t.Fatalf("insert agent %s: %v", id, err)
	}
}

func pagesGrantRows(t *testing.T, h *PageHandler, pageID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id = ?`, pageID).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	return n
}

// ── Rule 1: only a human issues a grant (§7.1b rule 1) ─────────────────────

// TestPageGrants_OnlyAHumanIssuesAGrant covers the rule that closes the
// escalation path where "an injected agent grows its own blast radius one
// grant at a time" — including the case the PRD calls out by name: an agent
// granting to an agent in its OWN crew.
//
// Every arm sends a request that would otherwise succeed: the caller context
// is the workspace OWNER, the body is valid, the page exists. The only
// difference is the mark of an agent on the request.
func TestPageGrants_OnlyAHumanIssuesAGrant(t *testing.T) {
	h, _, wsID, userID, pageID := pagesGrantFixture(t, "")
	pagesSeedAgent(t, h, wsID, "agt-watcher", "watcher", "crew-lookout")
	pagesSeedAgent(t, h, wsID, "agt-twin", "twin", "crew-lookout")

	body := `{"subject_type":"agent","subject":"twin","level":"read"}`

	// The sanity control: the same call from a human is accepted. Without it,
	// every arm below could be passing for the wrong reason.
	pagesGrant(t, h, wsID, userID, "fleet-201", body)
	if n := pagesGrantRows(t, h, pageID); n != 1 {
		t.Fatalf("the control grant was not stored: %d rows", n)
	}
	if rr := pagesGrantCall(t, h, "DELETE",
		"/api/v1/pages/fleet-201/grants?subject_type=agent&subject=twin", wsID, userID, "OWNER", "fleet-201", ""); rr.Code != http.StatusOK {
		t.Fatalf("control revoke: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	arms := []struct {
		name    string
		prepare func(*http.Request) *http.Request
	}{
		{
			// §7.1b rule 1's own scenario, and the strongest form of it: an
			// agent whose call has been proxied under a human's identity (the
			// shape internal_routines.go builds for pipelines). The acting
			// agent slug is the sidecar's own unforgeable marker, and it is
			// enough on its own — the crew relationship makes no difference,
			// because "not even to an agent in its own crew".
			name: "an agent granting to an agent in its own crew",
			prepare: func(r *http.Request) *http.Request {
				r.Header.Set(actingAgentSlugHeader, "watcher")
				return r
			},
		},
		{
			name: "a caller holding the raw internal token",
			prepare: func(r *http.Request) *http.Request {
				r.Header.Set("X-Internal-Token", "crwv1.whatever")
				return r
			},
		},
		{
			name: "a request the internal wrapper bound to a workspace",
			prepare: func(r *http.Request) *http.Request {
				return r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, "test-workspace-id"))
			},
		},
		{
			name: "a request the internal wrapper bound to a crew",
			prepare: func(r *http.Request) *http.Request {
				return r.WithContext(context.WithValue(r.Context(), ctxInternalTokenCrew, "crew-lookout"))
			},
		},
		{
			// Absence must deny (middleware.go's own contract): a request that
			// never went through RequireAuth carries no auth kind, and a
			// handler that read that as "human" would be trusting a context
			// somebody else assembled.
			name: "a request that never passed RequireAuth",
			prepare: func(r *http.Request) *http.Request {
				return r.WithContext(context.WithValue(r.Context(), ctxAuthKind, ""))
			},
		},
	}

	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			for _, call := range []struct {
				method, target string
				body           string
			}{
				{"PUT", "/api/v1/pages/fleet-201/grants", body},
				{"DELETE", "/api/v1/pages/fleet-201/grants?subject_type=agent&subject=twin", ""},
			} {
				req := arm.prepare(pagesGrantRequest(t, call.method, call.target, wsID, userID, "OWNER", call.body))
				req.SetPathValue("slug", "fleet-201")
				rr := httptest.NewRecorder()
				if call.method == "PUT" {
					h.PutGrant(rr, req)
				} else {
					h.DeleteGrant(rr, req)
				}
				if rr.Code != http.StatusForbidden {
					t.Fatalf("%s: status = %d, want 403 — only a human issues a grant (§7.1b rule 1); body: %s",
						call.method, rr.Code, rr.Body.String())
				}
				if !strings.Contains(rr.Body.String(), "human") {
					t.Errorf("%s: the refusal does not say why: %s", call.method, rr.Body.String())
				}
			}
			if n := pagesGrantRows(t, h, pageID); n != 0 {
				t.Errorf("a refused agent grant left %d row(s) behind", n)
			}
		})
	}
}

// ── Rule 2: authority is the issuer's, at USE time (§7.1b) ─────────────────

// TestPageGrants_AuthorityNarrowsWithTheIssuerAtUseTime is the rule the PRD
// calls the invariant that makes agent grants safe: "A grant to an agent is
// evaluated against the granting human's own rights AT USE TIME, not at grant
// time — if that human loses access to a crew, every agent grant they issued
// narrows with them."
//
// Each arm grants a working authority, takes something away from the ISSUER —
// never from the grantee — and asserts the authority is gone on the very next
// request. The row is checked to be still present each time: this is a
// narrowing, not a deletion, and a design that deleted rows would be a sweep
// with a window.
func TestPageGrants_AuthorityNarrowsWithTheIssuerAtUseTime(t *testing.T) {
	t.Run("the issuer leaves the workspace", func(t *testing.T) {
		h, _, wsID, ownerID, pageID := pagesGrantFixture(t, "")
		pagesSeedUser(t, h, wsID, "issuer", "issuer@example.com", "ADMIN")
		pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")

		// The ADMIN issues, not the page owner: the point is the ISSUER's
		// standing, whoever they are.
		rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, "issuer", "ADMIN", "fleet-201",
			`{"subject_type":"user","subject":"grantee@example.com","level":"produce","panels":["sluzby"]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("grant: status = %d, body: %s", rr.Code, rr.Body.String())
		}
		if push := pagesPush(t, h, wsID, "grantee", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); push.Code != http.StatusOK {
			t.Fatalf("the grant did not work before the issuer left: %d %s", push.Code, push.Body.String())
		}

		if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = 'issuer'`, wsID); err != nil {
			t.Fatalf("remove issuer from workspace: %v", err)
		}

		if push := pagesPush(t, h, wsID, "grantee", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); push.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 — a grant whose issuer left the workspace is authority delegated by nobody; body: %s",
				push.Code, push.Body.String())
		}
		if n := pagesGrantRows(t, h, pageID); n != 1 {
			t.Errorf("the grant row count is %d; the rule NARROWS a grant at use time, it does not sweep the row away", n)
		}
		grants := pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")
		if len(grants) != 1 {
			t.Fatalf("the ACL listing hid the inert row: %+v", grants)
		}
		if live, _ := grants[0]["live"].(bool); live {
			t.Errorf("the listing still reports the grant as live: %+v", grants[0])
		}
		if reason, _ := grants[0]["inert_reason"].(string); !strings.Contains(reason, "member of this workspace") {
			t.Errorf("inert_reason = %q, want it to name the issuer's lost membership", reason)
		}
	})

	t.Run("the issuer is demoted out of ADMIN", func(t *testing.T) {
		h, _, wsID, _, pageID := pagesGrantFixture(t, "")
		pagesSeedUser(t, h, wsID, "issuer", "issuer@example.com", "ADMIN")
		pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")

		rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, "issuer", "ADMIN", "fleet-201",
			`{"subject_type":"user","subject":"grantee@example.com","level":"produce","panels":["sluzby"]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("grant: status = %d, body: %s", rr.Code, rr.Body.String())
		}
		if push := pagesPush(t, h, wsID, "grantee", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); push.Code != http.StatusOK {
			t.Fatalf("the grant did not work before the demotion: %d %s", push.Code, push.Body.String())
		}

		if _, err := h.db.Exec(`UPDATE workspace_members SET role = 'MEMBER' WHERE workspace_id = ? AND user_id = 'issuer'`, wsID); err != nil {
			t.Fatalf("demote issuer: %v", err)
		}

		if push := pagesPush(t, h, wsID, "grantee", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); push.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 — a demoted issuer cannot keep delegating authority they no longer hold; body: %s",
				push.Code, push.Body.String())
		}
		if n := pagesGrantRows(t, h, pageID); n != 1 {
			t.Errorf("grant rows = %d, want the row still present but inert", n)
		}
	})

	t.Run("the issuer loses access to the owning crew", func(t *testing.T) {
		// The PRD's own sentence, literally: a crew-owned page, administered
		// by a member of that crew, who is then removed from the crew.
		h, _, wsID, ownerID, pageID := pagesGrantFixture(t, "crew/lookout")
		pagesSeedUser(t, h, wsID, "issuer", "issuer@example.com", "MEMBER")
		pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")
		if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm-issuer', 'crew-lookout', 'issuer')`); err != nil {
			t.Fatalf("add issuer to crew: %v", err)
		}

		rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, "issuer", "MEMBER", "fleet-201",
			`{"subject_type":"user","subject":"grantee@example.com","level":"write"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("a crew member administering their crew's page was refused: %d %s", rr.Code, rr.Body.String())
		}
		page, err := h.loadPage(context.Background(), wsID, "fleet-201")
		if err != nil {
			t.Fatalf("load page: %v", err)
		}
		if !h.mayEditSpec(context.Background(), wsID, "grantee", "MEMBER", page) {
			t.Fatal("the write grant did not work before the issuer left the crew")
		}

		if _, err := h.db.Exec(`DELETE FROM crew_members WHERE crew_id = 'crew-lookout' AND user_id = 'issuer'`); err != nil {
			t.Fatalf("remove issuer from crew: %v", err)
		}

		if h.mayEditSpec(context.Background(), wsID, "grantee", "MEMBER", page) {
			t.Error("the write grant survived its issuer losing access to the owning crew — " +
				"§7.1b: every grant they issued narrows with them")
		}
		if n := pagesGrantRows(t, h, pageID); n != 1 {
			t.Errorf("grant rows = %d, want the row still present but inert", n)
		}
		grants := pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")
		if len(grants) != 1 {
			t.Fatalf("grants listing = %+v", grants)
		}
		if live, _ := grants[0]["live"].(bool); live {
			t.Errorf("the listing still reports the grant as live: %+v", grants[0])
		}
	})
}

// ── Rule 3: a grant widens the page, never a crew's data (§7.1 rule 3) ─────

// TestPageGrants_WidenTheGrantNeverTheCrewsData: "A grantee still sees only
// the panels their own crew membership and workspace role already permit; a
// page owner cannot use a grant to leak their crew's panel to somebody outside
// it."
//
// The grantee here holds `write` — the strongest verb there is — and is still
// shown the sealed placeholder, because the two authorities are separate.
func TestPageGrants_WidenTheGrantNeverTheCrewsData(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MEMBER")

	// Real data on both panels, so there is something to leak.
	if rr := pagesPush(t, h, wsID, ownerID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("seed push: %d %s", rr.Code, rr.Body.String())
	}
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"outsider@example.com","level":"write"}`)

	doc := pagesGet(t, h, wsID, "outsider", "MEMBER", "fleet-201")
	panel := pagesPanel(t, doc, "sluzby")

	if sealed, _ := panel["sealed"].(bool); !sealed {
		t.Fatalf("a write grantee outside crew/lookout was served the panel itself: %s", mustPagesJSON(t, panel))
	}
	for _, leaked := range []string{"data", "schema", "producer", "sla_seconds", "state"} {
		if _, present := panel[leaked]; present {
			t.Errorf("the sealed placeholder carries %q; §11b decision 14 pins it to {panel_id, span, sealed, owner_crew_name}: %s",
				leaked, mustPagesJSON(t, panel))
		}
	}
	if strings.Contains(mustPagesJSON(t, doc), "200 OK") {
		t.Error("the payload of a crew panel reached a grantee outside that crew")
	}
	// The page itself is reachable, and the slot still holds its place — §2.3:
	// the grid must not reflow into a different shape per viewer.
	if name, _ := panel["owner_crew_name"].(string); name != "Lookout" {
		t.Errorf("owner_crew_name = %v, want the owning crew's display name", panel["owner_crew_name"])
	}
}

// ── Rule 4: a produce scope is per panel (§7.1b) ───────────────────────────

// TestPageGrants_ProduceScopeIsHonouredPerPanel drives the scope through the
// SURFACE rather than through hand-written SQL: "an agent granted produce on
// one panel cannot overwrite another agent's panel on the same page".
func TestPageGrants_ProduceScopeIsHonouredPerPanel(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "producer", "producer@example.com", "MEMBER")

	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"user","subject":"producer@example.com","level":"produce","panels":["sluzby"]}`)

	if rr := pagesPush(t, h, wsID, "producer", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("the granted panel was refused: %d %s", rr.Code, rr.Body.String())
	}
	if rr := pagesPush(t, h, wsID, "producer", "MEMBER", "fleet-201", "zatizeni", pagesMetricPayload); rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a produce grant scoped to sluzby must not reach zatizeni; body: %s",
			rr.Code, rr.Body.String())
	}
	// And the panel it could not write is untouched.
	panel := pagesPanel(t, pagesGet(t, h, wsID, ownerID, "OWNER", "fleet-201"), "zatizeni")
	if state, _ := panel["state"].(string); state != string(pages.StateNeverProduced) {
		t.Errorf("zatizeni state = %v after a refused push, want never_produced", panel["state"])
	}

	// A scope naming a panel the page does not have is refused at grant time:
	// a grant that authorises nothing is one somebody believes worked.
	rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, ownerID, "OWNER", "fleet-201",
		`{"subject_type":"user","subject":"producer@example.com","level":"produce","panels":["neexistuje"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("a produce scope naming an unknown panel: status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	// And a panel scope on a level that has no scope is refused too — the
	// database's own CHECK, answered as a message instead of a constraint.
	rr = pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, ownerID, "OWNER", "fleet-201",
		`{"subject_type":"user","subject":"producer@example.com","level":"read","panels":["sluzby"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("a panel scope on a read grant: status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
}

// ── Rule 5: every grant change is journalled (§7.1b) ───────────────────────

// TestPageGrants_ChangesAreJournalled — "an ACL nobody can audit is not a
// security control". Both verbs, actor and subject on each.
func TestPageGrants_ChangesAreJournalled(t *testing.T) {
	h, spy, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedAgent(t, h, wsID, "agt-watcher", "watcher", "crew-lookout")

	pagesGrant(t, h, wsID, ownerID, "fleet-201",
		`{"subject_type":"agent","subject":"watcher","level":"produce","panels":["sluzby"]}`)

	added := spy.firstOfType(journal.EntryPageGrantAdded)
	if added == nil {
		t.Fatalf("no %s entry", journal.EntryPageGrantAdded)
	}
	if added.ActorID != ownerID {
		t.Errorf("actor = %q, want the human who issued it (%q)", added.ActorID, ownerID)
	}
	if added.ActorType != journal.ActorUser {
		t.Errorf("actor type = %q, want %q — only a human issues a grant", added.ActorType, journal.ActorUser)
	}
	for field, want := range map[string]string{
		"page":         "fleet-201",
		"subject_type": "agent",
		"subject":      "watcher",
		"subject_id":   "agt-watcher",
		"level":        "produce",
	} {
		if got, _ := added.Payload[field].(string); got != want {
			t.Errorf("added payload %s = %v, want %q", field, added.Payload[field], want)
		}
	}
	if panels, _ := added.Payload["panels"].([]string); len(panels) != 1 || panels[0] != "sluzby" {
		t.Errorf("added payload panels = %v, want the scope that was granted", added.Payload["panels"])
	}

	rr := pagesGrantCall(t, h, "DELETE",
		"/api/v1/pages/fleet-201/grants?subject_type=agent&subject=watcher", wsID, ownerID, "OWNER", "fleet-201", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	removed := spy.firstOfType(journal.EntryPageGrantRemoved)
	if removed == nil {
		t.Fatalf("no %s entry", journal.EntryPageGrantRemoved)
	}
	if removed.ActorID != ownerID {
		t.Errorf("removal actor = %q, want the human who revoked it", removed.ActorID)
	}
	for field, want := range map[string]string{
		"subject_type": "agent",
		"subject":      "watcher",
		"level":        "produce",
	} {
		if got, _ := removed.Payload[field].(string); got != want {
			t.Errorf("removed payload %s = %v, want %q", field, removed.Payload[field], want)
		}
	}
}

// ── Rule 6: write is arrangement, never content (§7.1b rule 2) ─────────────

// TestPageGrants_AgentWriteArrangesWithoutReading is the rule's two halves in
// one test, because either half alone is a different feature:
//
//	"An agent with `write` may place a panel owned by a crew it cannot see. It
//	 does not receive that panel's data — the server filters it exactly as for
//	 any other viewer and the agent sees the sealed placeholder. This is what
//	 lets an agent assemble a cross-crew page for a team whose numbers it is
//	 not entitled to read."
//
// The agent belongs to crew/engine, and the page carries a panel owned by
// crew/lookout with real data on it.
func TestPageGrants_AgentWriteArrangesWithoutReading(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedAgent(t, h, wsID, "agt-arranger", "arranger", "crew-engine")

	if rr := pagesPush(t, h, wsID, ownerID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("seed push: %d %s", rr.Code, rr.Body.String())
	}
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"arranger","level":"write"}`)

	ctx := context.Background()
	rec, err := h.loadPage(ctx, wsID, "fleet-201")
	if err != nil {
		t.Fatalf("load page: %v", err)
	}

	// Half one — arrangement. The agent may edit the spec.
	if !h.agentMayEditSpec(ctx, wsID, rec, "agt-arranger") {
		t.Fatal("an agent holding a live `write` grant may not edit the spec — that is the orchestration role the feature exists for")
	}

	// Half two — content. The same agent, on the same page, is served the
	// sealed placeholder for the panel owned by the crew it is not in.
	viewer, err := h.agentViewer(ctx, wsID, "agt-arranger")
	if err != nil {
		t.Fatalf("agent viewer: %v", err)
	}
	panels, err := h.loadPanels(ctx, wsID, rec.ID)
	if err != nil {
		t.Fatalf("load panels: %v", err)
	}
	doc := h.pageDocument(ctx, rec, panels, viewer)
	encoded := mustPagesJSON(t, doc)
	if strings.Contains(encoded, "200 OK") {
		t.Errorf("an agent with `write` received the data of a crew it cannot see — "+
			"`write` is authority over arrangement, never over content: %s", encoded)
	}

	var sealedSluzby, wholeZatizeni bool
	for _, p := range doc.Panels {
		switch panel := p.(type) {
		case pageSealedPanelWire:
			if panel.PanelID == "sluzby" {
				sealedSluzby = true
				if panel.Span != 8 {
					t.Errorf("the sealed slot lost its width (span = %d, want 8); §2.3: the grid must not reflow per viewer", panel.Span)
				}
			}
		case pagePanelWire:
			if panel.ID == "zatizeni" {
				wholeZatizeni = true
			}
		}
	}
	if !sealedSluzby {
		t.Errorf("the crew/lookout panel was not sealed for an agent of crew/engine: %s", encoded)
	}
	if !wholeZatizeni {
		t.Errorf("the agent's OWN crew's panel was withheld; the grant widens the page, and membership still reaches the panel: %s", encoded)
	}

	// The narrowing rule reaches the agent path too: the human who issued the
	// grant loses their standing, and the agent's authority goes with it.
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, ownerID); err != nil {
		t.Fatalf("remove issuer: %v", err)
	}
	if h.agentMayEditSpec(ctx, wsID, rec, "agt-arranger") {
		t.Error("an agent's `write` grant outlived its issuer's workspace membership (§7.1b)")
	}
}

// ── The surface: who may administer it, and symmetry ───────────────────────

// TestPageGrants_OnlyTheOwnerOrAnAdminAdministersThem — §7.1 rule 3: "Grants
// are issued by the page owner or by a workspace ADMIN/OWNER". A `write`
// grantee is explicitly NOT one of those: an agent or a person who may
// rearrange the page must not be able to widen who reaches it, or the first
// grant issued is the last one the owner controls.
func TestPageGrants_OnlyTheOwnerOrAnAdminAdministersThem(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "writer", "writer@example.com", "MEMBER")
	pagesSeedUser(t, h, wsID, "bystander", "bystander@example.com", "MEMBER")
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"writer@example.com","level":"write"}`)

	for _, caller := range []string{"writer", "bystander"} {
		for _, call := range []struct{ method, target, body string }{
			{"GET", "/api/v1/pages/fleet-201/grants", ""},
			{"PUT", "/api/v1/pages/fleet-201/grants", `{"subject_type":"user","subject":"bystander@example.com","level":"read"}`},
			{"DELETE", "/api/v1/pages/fleet-201/grants?subject_type=user&subject=writer@example.com", ""},
		} {
			rr := pagesGrantCall(t, h, call.method, call.target, wsID, caller, "MEMBER", "fleet-201", call.body)
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s %s as %q: status = %d, want 403 — widening access is not itself grantable; body: %s",
					call.method, call.target, caller, rr.Code, rr.Body.String())
			}
		}
	}
	// One row, unchanged: nothing above was allowed to write.
	if grants := pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201"); len(grants) != 1 {
		t.Errorf("grants = %+v, want the single grant the owner issued", grants)
	}
}

// TestPageGrants_RevokeIsSymmetricWithGrant — §11b decision 13: "fully
// symmetric with grant. An asymmetric revoke is how a grant becomes impossible
// to remove." Every subject kind, by the same reference that granted it; a
// revoke with no level removes every level the subject holds; a revoke naming
// a level removes only that one.
func TestPageGrants_RevokeIsSymmetricWithGrant(t *testing.T) {
	h, _, wsID, ownerID, _ := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")
	pagesSeedAgent(t, h, wsID, "agt-watcher", "watcher", "crew-lookout")

	subjects := []struct{ kind, ref string }{
		{"user", "grantee@example.com"},
		{"crew", "engine"},
		{"agent", "watcher"},
	}
	for _, s := range subjects {
		pagesGrant(t, h, wsID, ownerID, "fleet-201",
			`{"subject_type":"`+s.kind+`","subject":"`+s.ref+`","level":"read"}`)
	}
	if got := len(pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")); got != 3 {
		t.Fatalf("grants after granting three subjects = %d, want 3", got)
	}
	for _, s := range subjects {
		rr := pagesGrantCall(t, h, "DELETE",
			"/api/v1/pages/fleet-201/grants?subject_type="+s.kind+"&subject="+s.ref, wsID, ownerID, "OWNER", "fleet-201", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("revoke %s/%s: status = %d, body: %s", s.kind, s.ref, rr.Code, rr.Body.String())
		}
	}
	if got := len(pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")); got != 0 {
		t.Fatalf("grants after revoking all three = %d, want 0", got)
	}

	// Two levels for one subject: an unqualified revoke takes both, a
	// qualified one takes exactly the level it names.
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"watcher","level":"read"}`)
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"watcher","level":"produce","panels":["sluzby"]}`)
	rr := pagesGrantCall(t, h, "DELETE",
		"/api/v1/pages/fleet-201/grants?subject_type=agent&subject=watcher&level=produce", wsID, ownerID, "OWNER", "fleet-201", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("levelled revoke: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	grants := pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")
	if len(grants) != 1 {
		t.Fatalf("grants after a levelled revoke = %+v, want only the read grant", grants)
	}
	if level, _ := grants[0]["level"].(string); level != "read" {
		t.Errorf("the levelled revoke removed the wrong row: %+v", grants[0])
	}
	rr = pagesGrantCall(t, h, "DELETE",
		"/api/v1/pages/fleet-201/grants?subject_type=agent&subject=watcher", wsID, ownerID, "OWNER", "fleet-201", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("unqualified revoke: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if got := len(pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201")); got != 0 {
		t.Errorf("grants after the unqualified revoke = %d, want 0", got)
	}

	// Revoking what is not there is a 404 rather than a silent success: an
	// operator running this in an incident needs to know it did nothing.
	rr = pagesGrantCall(t, h, "DELETE",
		"/api/v1/pages/fleet-201/grants?subject_type=agent&subject=watcher", wsID, ownerID, "OWNER", "fleet-201", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("revoking a grant that does not exist: status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

// TestPageGrants_ReIssuingReanchorsTheIssuer — re-running the same grant is
// the same state (PUT, not POST), and it moves the authority behind it to
// whoever vouches for it now. That is the §7.1b invariant working as intended:
// a grant is only ever as strong as the human standing behind it TODAY.
func TestPageGrants_ReIssuingReanchorsTheIssuer(t *testing.T) {
	h, _, wsID, ownerID, pageID := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, wsID, "issuer", "issuer@example.com", "ADMIN")
	pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")

	body := `{"subject_type":"user","subject":"grantee@example.com","level":"produce","panels":["sluzby"]}`
	if rr := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", wsID, "issuer", "ADMIN", "fleet-201", body); rr.Code != http.StatusOK {
		t.Fatalf("first grant: %d %s", rr.Code, rr.Body.String())
	}
	pagesGrant(t, h, wsID, ownerID, "fleet-201", body)
	if n := pagesGrantRows(t, h, pageID); n != 1 {
		t.Fatalf("re-issuing the same grant made %d rows; PUT is idempotent", n)
	}

	// The first issuer leaves; the grant survives, because the second issuer
	// is the one standing behind it now.
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = 'issuer'`, wsID); err != nil {
		t.Fatalf("remove first issuer: %v", err)
	}
	if rr := pagesPush(t, h, wsID, "grantee", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the grant is anchored to the human who re-issued it; body: %s", rr.Code, rr.Body.String())
	}
}
