package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5260, R71 §0.14/1: delegating data Edit to agents is owner-only by every
// route, not only by an agent `produce` grant. A crew `produce` grant lets the
// crew's agents and routines push the page's data, and naming `agent/<slug>`
// as a panel's producer lets that agent push it. Both are owner-only; an
// existing one the owner did not make is retired for good with an audit entry
// and a visible reason, and the owner re-grants.

const pagesProducerAgentSlug = "writer"

func pagesProducerFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, _, wsID, ownerID, pageID := newInternalPagesFixture(t, "fleet-201")
	pagesSeedUser(t, h, wsID, "admin", "admin@example.com", "ADMIN")
	pagesSeedUser(t, h, wsID, "editor", "editor@example.com", "MEMBER")
	// The editor sees every panel, so only the producer rule can refuse them.
	pagesSeedCrewMember(t, h, pagesCrewID, "editor")
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug) VALUES ('agent-writer', ?, ?, 'Writer', ?)`,
		wsID, pagesCrewID, pagesProducerAgentSlug); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"editor@example.com","level":"write"}`)
	return h, wsID, ownerID, pageID
}

func pagesPatchAs(t *testing.T, h *PageHandler, wsID, userID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesGrantRequest(t, "PATCH", "/api/v1/pages/fleet-201", wsID, userID, role, body)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

// pagesPanelsWithProducer is the fixture's three panels with sluzby produced
// by `producer` and titled `title`.
func pagesPanelsWithProducer(producer, title string) string {
	return `{"panels": [
		{"id": "sluzby", "schema": "status.v1", "title": "` + title + `",
		 "owner": "crew/lookout", "producer": "` + producer + `", "sla_seconds": 30, "span": 6},
		{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
		 "owner": "crew/lookout", "producer": "routine/` + pagesOtherSlug + `", "sla_seconds": 30, "span": 6},
		{"id": "tabulka", "schema": "table.v1", "title": "Prehled",
		 "owner": "crew/lookout", "producer": "routine/` + pagesRoutineSlug + `", "sla_seconds": 30, "span": 12}
	]}`
}

func pagesAgentPush(t *testing.T, h *PageHandler, wsID string) int {
	t.Helper()
	body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
	body["author_run_id"] = ""
	body["agent_id"] = "agent-writer"
	return pagesInternalPush(t, h, "fleet-201", body).Code
}

func pagesCrewPush(t *testing.T, h *PageHandler, wsID string) int {
	t.Helper()
	body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
	body["author_run_id"] = pagesRunOther // not sluzby's producer: only a grant admits it
	return pagesInternalPush(t, h, "fleet-201", body).Code
}

func pagesProducerRetired(t *testing.T, h *PageHandler, pageID string) bool {
	t.Helper()
	var retired bool
	if err := h.db.QueryRow(`SELECT producer_retired_at IS NOT NULL FROM page_panels WHERE page_id=? AND panel_id='sluzby'`, pageID).Scan(&retired); err != nil {
		t.Fatalf("read producer designation: %v", err)
	}
	return retired
}

func pagesAuditCount(t *testing.T, h *PageHandler, action, pageID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action=? AND entity_id=?`, action, pageID).Scan(&n); err != nil {
		t.Fatalf("count %s audits: %v", action, err)
	}
	return n
}

func TestCrewProduceDelegationIsOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name, caller, role, body string
		want                     int
	}{
		{"page owner grants a crew produce", "", "OWNER", `{"subject_type":"crew","subject":"lookout","level":"produce"}`, http.StatusOK},
		{"workspace admin may not grant a crew produce", "admin", "ADMIN", `{"subject_type":"crew","subject":"lookout","level":"produce"}`, http.StatusForbidden},
		{"Edit grantee may not grant a crew produce", "editor", "MEMBER", `{"subject_type":"crew","subject":"lookout","level":"produce"}`, http.StatusForbidden},
		{"admin still grants a crew read", "admin", "ADMIN", `{"subject_type":"crew","subject":"lookout","level":"read"}`, http.StatusOK},
		{"admin still grants a crew write", "admin", "ADMIN", `{"subject_type":"crew","subject":"lookout","level":"write"}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, wsID, ownerID, _ := pagesProducerFixture(t)
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
}

func TestNonOwnerCrewProduceIsRetiredForGood(t *testing.T) {
	h, wsID, ownerID, pageID := pagesProducerFixture(t)
	if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id, granted_at)
		VALUES (?, 'crew', ?, 'produce', 'admin', '2026-11-01T00:00:00Z')`, pageID, pagesCrewID); err != nil {
		t.Fatalf("seed admin-issued crew produce: %v", err)
	}
	if code := pagesCrewPush(t, h, wsID); code != http.StatusForbidden {
		t.Fatalf("push on an admin-issued crew produce grant: %d, want 403", code)
	}
	if n := pagesAuditCount(t, h, "page.agent_grant.retired_not_owner", pageID); n != 1 {
		t.Fatalf("retirement audits = %d, want 1", n)
	}
	for _, g := range pagesGrantList(t, h, wsID, ownerID, "OWNER", "fleet-201") {
		if g["subject_type"] != "crew" {
			continue
		}
		if live, _ := g["live"].(bool); live {
			t.Errorf("admin-issued crew produce listed live: %+v", g)
		}
		if reason, _ := g["inert_reason"].(string); !strings.Contains(reason, "owner") || !strings.Contains(reason, "new grant") {
			t.Errorf("inert_reason = %q", reason)
		}
	}
	if _, err := h.db.Exec(`UPDATE pages SET owner_user_id='admin' WHERE id=?`, pageID); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if code := pagesCrewPush(t, h, wsID); code != http.StatusForbidden {
		t.Fatalf("the issuer becoming owner revived the crew produce grant: %d", code)
	}
	pagesGrant(t, h, wsID, "admin", "fleet-201", `{"subject_type":"crew","subject":"lookout","level":"produce"}`)
	if code := pagesCrewPush(t, h, wsID); code != http.StatusOK {
		t.Fatalf("the then-owner's new crew produce grant does not admit the push: %d", code)
	}
}

func TestAgentProducerDesignationIsOwnerOnly(t *testing.T) {
	t.Run("only the owner names an agent producer", func(t *testing.T) {
		for _, tc := range []struct {
			name, caller, role string
			want               int
		}{
			{"workspace admin", "admin", "ADMIN", http.StatusForbidden},
			{"Edit grantee", "editor", "MEMBER", http.StatusForbidden},
			{"page owner", "", "OWNER", http.StatusOK},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h, wsID, ownerID, pageID := pagesProducerFixture(t)
				caller := tc.caller
				if caller == "" {
					caller = ownerID
				}
				rr := pagesPatchAs(t, h, wsID, caller, tc.role, pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?"))
				if rr.Code != tc.want {
					t.Fatalf("status = %d, want %d; body: %s", rr.Code, tc.want, rr.Body.String())
				}
				if tc.want == http.StatusForbidden {
					if !strings.Contains(rr.Body.String(), "owner") {
						t.Errorf("the refusal must say only the owner names an agent producer; body: %s", rr.Body.String())
					}
					return
				}
				if pagesProducerRetired(t, h, pageID) {
					t.Fatal("the owner's agent producer was retired")
				}
				if code := pagesAgentPush(t, h, wsID); code != http.StatusOK {
					t.Fatalf("the owner's agent producer cannot push: %d", code)
				}
			})
		}
	})

	t.Run("an admin edit that keeps the owner's agent producer leaves it live", func(t *testing.T) {
		h, wsID, ownerID, pageID := pagesProducerFixture(t)
		if rr := pagesPatchAs(t, h, wsID, ownerID, "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?")); rr.Code != http.StatusOK {
			t.Fatalf("owner names the producer: %d %s", rr.Code, rr.Body.String())
		}
		if rr := pagesPatchAs(t, h, wsID, "admin", "ADMIN", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Retitled")); rr.Code != http.StatusOK {
			t.Fatalf("admin retitles: %d %s", rr.Code, rr.Body.String())
		}
		if pagesProducerRetired(t, h, pageID) || pagesAgentPush(t, h, wsID) != http.StatusOK {
			t.Fatal("an admin edit that did not touch the producer ended the owner's designation")
		}
	})

	t.Run("a non-owner designation is retired for good, visibly, and the owner re-grants", func(t *testing.T) {
		h, wsID, ownerID, pageID := pagesProducerFixture(t)
		if rr := pagesPatchAs(t, h, wsID, ownerID, "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?")); rr.Code != http.StatusOK {
			t.Fatalf("owner names the producer: %d %s", rr.Code, rr.Body.String())
		}
		// As a row written before the rule, or by any other path, would be.
		if _, err := h.db.Exec(`UPDATE page_panels SET producer_designated_by_user_id='admin' WHERE page_id=? AND panel_id='sluzby'`, pageID); err != nil {
			t.Fatalf("re-attribute the designation: %v", err)
		}
		if !pagesProducerRetired(t, h, pageID) {
			t.Fatal("a non-owner agent producer designation was stored live")
		}
		if n := pagesAuditCount(t, h, "page.agent_producer.retired_not_owner", pageID); n != 1 {
			t.Fatalf("producer retirement audits = %d, want 1", n)
		}
		if code := pagesAgentPush(t, h, wsID); code != http.StatusForbidden {
			t.Fatalf("push by a retired agent producer: %d, want 403", code)
		}
		rec, err := h.loadPage(context.Background(), wsID, "fleet-201")
		if err != nil {
			t.Fatalf("load page: %v", err)
		}
		panels, err := h.loadPanelsIn(context.Background(), wsID, pageScope{where: "p.id = ?", args: []any{rec.ID}})
		if err != nil {
			t.Fatalf("load panels: %v", err)
		}
		var fault string
		for _, p := range panels[rec.ID] {
			if p.PanelID == "sluzby" {
				fault = p.Fault
			}
		}
		if !strings.Contains(fault, "owner") {
			t.Errorf("panel fault = %q, want the reason the producer was ended", fault)
		}

		// Its designator becoming owner revives nothing.
		if _, err := h.db.Exec(`UPDATE pages SET owner_user_id='admin' WHERE id=?`, pageID); err != nil {
			t.Fatalf("transfer: %v", err)
		}
		if pagesAgentPush(t, h, wsID) != http.StatusForbidden || !pagesProducerRetired(t, h, pageID) {
			t.Fatal("ownership revived a retired agent producer designation")
		}
		// An edit that keeps the producer is no re-designation.
		if rr := pagesPatchAs(t, h, wsID, "admin", "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Retitled")); rr.Code != http.StatusOK {
			t.Fatalf("owner retitles: %d %s", rr.Code, rr.Body.String())
		}
		if pagesAgentPush(t, h, wsID) != http.StatusForbidden {
			t.Fatal("a retitle revived a retired agent producer designation")
		}
		// The owner re-grants explicitly.
		pagesGrant(t, h, wsID, "admin", "fleet-201", `{"subject_type":"agent","subject":"`+pagesProducerAgentSlug+`","level":"produce","panels":["sluzby"]}`)
		if code := pagesAgentPush(t, h, wsID); code != http.StatusOK {
			t.Fatalf("the owner's explicit produce grant does not admit the agent: %d", code)
		}
	})

	t.Run("a transfer ends the previous owner's agent producer designation", func(t *testing.T) {
		h, wsID, ownerID, pageID := pagesProducerFixture(t)
		if rr := pagesPatchAs(t, h, wsID, ownerID, "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?")); rr.Code != http.StatusOK {
			t.Fatalf("owner names the producer: %d %s", rr.Code, rr.Body.String())
		}
		if _, err := h.db.Exec(`UPDATE pages SET owner_user_id='editor' WHERE id=?`, pageID); err != nil {
			t.Fatalf("transfer: %v", err)
		}
		if !pagesProducerRetired(t, h, pageID) || pagesAgentPush(t, h, wsID) != http.StatusForbidden {
			t.Fatal("the previous owner's agent producer survived the transfer")
		}
	})
}

// The page_versions spec the migration reads provenance from stores each panel
// under $.spec.panels with its producer string; pin that shape.
func TestPageVersionSpecCarriesPanelProducers(t *testing.T) {
	h, wsID, ownerID, pageID := pagesProducerFixture(t)
	if rr := pagesPatchAs(t, h, wsID, ownerID, "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?")); rr.Code != http.StatusOK {
		t.Fatalf("owner names the producer: %d %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_versions pv, json_each(pv.spec_json,'$.spec.panels') j
		WHERE pv.page_id=? AND json_extract(j.value,'$.id')='sluzby' AND json_extract(j.value,'$.producer')='agent/'||?`,
		pageID, pagesProducerAgentSlug).Scan(&n); err != nil || n == 0 {
		t.Fatalf("no version names sluzby's agent producer at $.spec.panels: n=%d err=%v", n, err)
	}
}
