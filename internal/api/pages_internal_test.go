package api

// Pages — the routine write path (#1945, docs/prd/pages.md §0, §7.1 rule 4,
// §7.1b, §11, §11b.6).
//
// The public push is tested in pages_handler_test.go with a human on the other
// end. Everything here is the half that only exists when there is NO human: the
// run is the identity, the body is an envelope rather than the payload, and the
// 64 KiB cap has to be enforced by the handler because /api/v1/internal/*
// bypasses BodyCap.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

// ── Fixture ────────────────────────────────────────────────────────────────

const (
	pagesRunNightly  = "run_nightly"
	pagesRunOther    = "run_other"
	pagesRoutineSlug = "nightly-probe"
	pagesOtherSlug   = "other-probe"
	pagesCrewID      = "crew-lookout"
)

// pagesInternalSpec is a two-panel page whose panels are produced by two
// DIFFERENT routines. Two, because every interesting question here is "may this
// run write THIS panel" and a one-panel page cannot ask it.
func pagesInternalSpec(slug string) string {
	return `{
		"slug": "` + slug + `",
		"name": "Flotila .201",
		"panels": [
			{"id": "sluzby", "schema": "status.v1", "title": "Jede to?",
			 "owner": "crew/lookout", "producer": "routine/` + pagesRoutineSlug + `",
			 "sla_seconds": 30, "span": 6},
			{"id": "zatizeni", "schema": "metric.v1", "title": "Zatizeni",
			 "owner": "crew/lookout", "producer": "routine/` + pagesOtherSlug + `",
			 "sla_seconds": 30, "span": 6},
			{"id": "tabulka", "schema": "table.v1", "title": "Prehled",
			 "owner": "crew/lookout", "producer": "routine/` + pagesRoutineSlug + `",
			 "sla_seconds": 30, "span": 12}
		]
	}`
}

// newInternalPagesFixture returns the handler, the journal spy, the workspace,
// the owner and the page id — over a database holding two routines and one run
// of each, so "the declared producer is calling" is a fact about server-side
// rows rather than about a string in the request.
func newInternalPagesFixture(t *testing.T, slug string) (*PageHandler, *pagesJournalSpy, string, string, string) {
	t.Helper()
	h, spy, _, wsID, userID := newPagesFixture(t)

	for _, r := range []struct{ id, routine, run string }{
		{"pl_nightly", pagesRoutineSlug, pagesRunNightly},
		{"pl_other", pagesOtherSlug, pagesRunOther},
	} {
		if _, err := h.db.Exec(`
			INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
			VALUES (?, ?, ?, ?, '{}', 'hash')`, r.id, wsID, r.routine, r.routine); err != nil {
			t.Fatalf("insert pipeline %s: %v", r.routine, err)
		}
		if _, err := h.db.Exec(`
			INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at)
			VALUES (?, ?, ?, ?, 'running', '2026-08-12T09:00:00Z')`,
			r.run, wsID, r.id, r.routine); err != nil {
			t.Fatalf("insert run %s: %v", r.run, err)
		}
	}

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesInternalSpec(slug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	pageID, _ := doc["id"].(string)
	return h, spy, wsID, userID, pageID
}

// pagesInternalPush drives the internal route the way the dispatcher does: the
// author's args merged with the identity fields crewshipBody injects.
func pagesInternalPush(t *testing.T, h *PageHandler, slug string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return pagesInternalPushRaw(t, h, slug, raw)
}

// pagesInternalPushRaw is the same, for bodies that must not go through
// json.Marshal — the oversize cases, where the point is the byte count.
func pagesInternalPushRaw(t *testing.T, h *PageHandler, slug string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/v1/internal/pages/"+slug+"/data", bytes.NewReader(raw))
	req.SetPathValue("page", slug)
	rr := httptest.NewRecorder()
	h.PushDataInternal(rr, req)
	return rr
}

// pagesInternalBody is the envelope for a well-formed push by the nightly
// routine: the author's three args plus the four fields the dispatcher owns.
func pagesInternalBody(wsID, panel string, data any) map[string]any {
	return map[string]any{
		"workspace_id":  wsID,
		"crew_id":       pagesCrewID,
		"author_run_id": pagesRunNightly,
		"panel":         panel,
		"data":          data,
	}
}

func pagesStatusData() map[string]any {
	return map[string]any{"items": []map[string]any{{"name": "api", "state": "ok", "label": "200 OK"}}}
}

// ── 1. The declared producer writes ────────────────────────────────────────

// §7.1 rule 4 — "Only the declared producer may write a panel's payload" — with
// no human and no grant anywhere. The panel names `routine/nightly-probe`, the
// injected run is a run OF that routine, and that is the whole authority: this
// is the PRD §0 path where a routine keeps a panel fresh unattended.
//
// It also pins the provenance §4 rule 5 requires: the run id comes from the
// dispatcher's injected field and lands in producer_run_id, so a page can say
// which run produced the number it is showing.
func TestPagesInternalPush_DeclaredProducerRoutineWrites(t *testing.T) {
	h, _, wsID, userID, _ := newInternalPagesFixture(t, "fleet-201")

	rr := pagesInternalPush(t, h, "fleet-201", pagesInternalBody(wsID, "sluzby", pagesStatusData()))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the panel names this routine as its producer; body: %s",
			rr.Code, rr.Body.String())
	}
	var resp struct {
		Accepted   bool   `json:"accepted"`
		Panel      string `json:"panel"`
		State      string `json:"state"`
		Provenance struct {
			Producer   string `json:"producer"`
			RunID      string `json:"run_id"`
			ProducedAt string `json:"produced_at"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rr.Body.String())
	}
	if !resp.Accepted || resp.Panel != "sluzby" {
		t.Errorf("response = %+v, want accepted for panel sluzby", resp)
	}
	if resp.State != string(pages.StateFresh) {
		t.Errorf("state = %q, want fresh — the payload was just written against the server's clock", resp.State)
	}
	if resp.Provenance.Producer != "routine/"+pagesRoutineSlug {
		t.Errorf("provenance.producer = %q, want the panel's declared producer", resp.Provenance.Producer)
	}
	if resp.Provenance.RunID != pagesRunNightly {
		t.Errorf("provenance.run_id = %q, want the injected run %q — a page has to be able to say "+
			"which run produced the number", resp.Provenance.RunID, pagesRunNightly)
	}

	// Stored, with the run attached — the column the UI reads provenance from.
	var payload, runID string
	if err := h.db.QueryRow(`
		SELECT d.payload_json, COALESCE(d.producer_run_id, '')
		FROM page_panel_data d JOIN page_panels p ON p.id = d.panel_id
		WHERE p.panel_id = 'sluzby'`).Scan(&payload, &runID); err != nil {
		t.Fatalf("no stored payload: %v", err)
	}
	if !strings.Contains(payload, "200 OK") {
		t.Errorf("stored payload = %s, want the pushed one", payload)
	}
	if runID != pagesRunNightly {
		t.Errorf("stored producer_run_id = %q, want %q", runID, pagesRunNightly)
	}

	// And the page shows it to a human through the ordinary read path.
	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if !strings.Contains(mustPagesJSON(t, panel["data"]), "200 OK") {
		t.Errorf("the page does not show what the routine wrote: %s", mustPagesJSON(t, panel))
	}
}

// ── 2. A panel it does not produce ─────────────────────────────────────────

// §7.1b rule 3 on the unattended path: 403, a journal entry, and a notification
// to the page owner — for a run that IS a legitimate routine in this workspace
// and simply is not this panel's producer. That is the interesting case: the
// caller is authenticated (it holds the internal token), so the only thing
// standing between it and another team's numbers is this check.
func TestPagesInternalPush_RefusesAPanelItDoesNotProduce(t *testing.T) {
	h, spy, wsID, userID, _ := newInternalPagesFixture(t, "fleet-201")

	body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
	body["author_run_id"] = pagesRunOther // a run of other-probe, which produces `zatizeni`

	rr := pagesInternalPush(t, h, "fleet-201", body)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — this run is not the panel's producer; body: %s",
			rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "routine/"+pagesRoutineSlug) {
		t.Errorf("the refusal must name the producer the panel declares, got %s", rr.Body.String())
	}

	entry := spy.firstOfType(EntryPageProduceDenied)
	if entry == nil {
		t.Fatalf("no %s journal entry; a refusal nobody can audit is not a security control",
			EntryPageProduceDenied)
	}
	if got, _ := entry.Payload["actor_run_id"].(string); got != pagesRunOther {
		t.Errorf("journal payload actor_run_id = %v, want the offending run — on this path the run "+
			"is often the only identity there is", entry.Payload["actor_run_id"])
	}
	if got, _ := entry.Payload["panel"].(string); got != "sluzby" {
		t.Errorf("journal payload panel = %v, want sluzby", entry.Payload["panel"])
	}

	var target, title string
	if err := h.db.QueryRow(`SELECT COALESCE(target_user_id, ''), title FROM inbox_items
		WHERE workspace_id = ? AND source_id LIKE 'page-produce-denied:%'`, wsID).Scan(&target, &title); err != nil {
		t.Fatalf("no notification for the page owner: %v", err)
	}
	if target != userID {
		t.Errorf("notification target = %q, want the page owner %q", target, userID)
	}

	// Nothing was stored: the panel is still as it was.
	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateNeverProduced) {
		t.Errorf("panel state = %v after a refused push, want never_produced", panel["state"])
	}
}

// ── 3. Produce grants ──────────────────────────────────────────────────────

// The other way in (§7.1b): a HUMAN granted this agent or this crew `produce`
// on the panel. Table-driven because the four cases differ only in the grant
// row, and it is the differences between them — the panel scope and the
// granting human's own reach — that carry the rule.
func TestPagesInternalPush_ProduceGrant(t *testing.T) {
	for _, tc := range []struct {
		name        string
		subjectType string
		subjectID   string
		panelIDs    any    // nil = every panel
		grantedBy   string // "" = the page owner (an OWNER of the workspace)
		agentID     string // the acting agent the dispatcher injects
		wantStatus  int
	}{
		{
			// A grant to the acting AGENT, scoped to this panel: the shape the
			// PRD's own CLI example issues.
			name: "agent grant covering the panel", subjectType: "agent", subjectID: "agent-writer",
			panelIDs: []string{"sluzby"}, agentID: "agent-writer", wantStatus: http.StatusOK,
		},
		{
			// §7.1b: "an agent granted produce on one panel cannot overwrite
			// another agent's panel on the same page".
			name: "agent grant scoped to another panel", subjectType: "agent", subjectID: "agent-writer",
			panelIDs: []string{"zatizeni"}, agentID: "agent-writer", wantStatus: http.StatusForbidden,
		},
		{
			// NULL panel_ids covers every panel, and a grant to the routine's
			// author CREW works with no acting agent at all.
			name: "crew grant covering every panel", subjectType: "crew", subjectID: pagesCrewID,
			panelIDs: nil, agentID: "", wantStatus: http.StatusOK,
		},
		{
			// The use-time narrowing (§7.1b): "an agent's authority is a subset
			// of the authorising human's ... if that human loses access to a
			// crew, every agent grant they issued narrows with them". This
			// grant's issuer is a MEMBER who is not in the panel's owning crew,
			// so they cannot see the panel and cannot delegate writing it.
			name: "granted by a human who cannot see the panel", subjectType: "agent", subjectID: "agent-writer",
			panelIDs: []string{"sluzby"}, grantedBy: "outsider", agentID: "agent-writer",
			wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, wsID, userID, pageID := newInternalPagesFixture(t, "fleet-201")
			if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('outsider', 'out@example.com', 'Outsider')`); err != nil {
				t.Fatalf("insert user: %v", err)
			}
			if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
				VALUES ('wm-outsider', ?, 'outsider', 'MEMBER')`, wsID); err != nil {
				t.Fatalf("insert membership: %v", err)
			}
			if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug)
				VALUES ('agent-writer', ?, ?, 'Writer', 'writer')`, wsID, pagesCrewID); err != nil {
				t.Fatalf("insert agent: %v", err)
			}
			grantedBy := tc.grantedBy
			if grantedBy == "" {
				grantedBy = userID
			}
			var panelIDs any
			if tc.panelIDs != nil {
				raw, err := json.Marshal(tc.panelIDs)
				if err != nil {
					t.Fatalf("marshal panel ids: %v", err)
				}
				panelIDs = string(raw)
			}
			if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, panel_ids, granted_by_user_id)
				VALUES (?, ?, ?, 'produce', ?, ?)`, pageID, tc.subjectType, tc.subjectID, panelIDs, grantedBy); err != nil {
				t.Fatalf("insert grant: %v", err)
			}

			// The run is one of the OTHER routine, so the grant is the only
			// thing that can let this through.
			body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
			body["author_run_id"] = pagesRunOther
			body["agent_id"] = tc.agentID

			rr := pagesInternalPush(t, h, "fleet-201", body)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

// ── 4. The body cap, which this path has to enforce itself ─────────────────

// §11b.6: "The size cap is enforced at the handler ... The same 422 must arrive
// on the sidecar path, where no client pre-check exists." And
// /api/v1/internal/* bypasses the global BodyCap middleware
// (crew_messaging.go:60), so a route on this prefix that does not bound its own
// body has no bound at all.
//
// Three cases, because the path has two bounds and they answer different
// questions: the payload cap the caller is OWED (64 KiB, judged by
// pages.ValidatePayload on `data` alone, so the envelope reports the same
// bytes_limit as the public route), and the envelope bound that stops the READ
// itself from being unbounded. A legal 64 KiB payload must survive both, or the
// envelope bound would have quietly lowered the documented cap.
func TestPagesInternalPush_BodyCapIsEnforced(t *testing.T) {
	// A table.v1 payload just under the cap, and VALID against its schema —
	// 200 rows is the row cap (§11b.12), so the size has to come from the cells.
	// The cell width is walked down until the document fits, which lands within
	// a few hundred bytes of the target: close enough that a handler which had
	// quietly lowered the effective cap would fail here.
	//
	// table.v1 rather than status.v1 because status caps at 200 items of 120 +
	// 200 characters, i.e. roughly 49 KiB — a schema that cannot reach the
	// payload cap cannot test it.
	nearCap := func(t *testing.T, target int) map[string]any {
		t.Helper()
		for cell := 500; cell > 0; cell-- {
			rows := make([]map[string]any, 200)
			for i := range rows {
				rows[i] = map[string]any{
					"sluzba": fmt.Sprintf("svc-%04d", i),
					"stav":   "ok",
					"detail": strings.Repeat("x", cell),
				}
			}
			payload := map[string]any{
				"columns": []map[string]any{
					{"key": "sluzba", "label": "Sluzba"},
					{"key": "stav", "label": "Stav"},
					{"key": "detail", "label": "Detail"},
				},
				"rows": rows,
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if len(raw) <= target {
				if len(raw) < target-1024 {
					t.Fatalf("fixture is only %d bytes; it has to sit just under %d to test the cap",
						len(raw), target)
				}
				return payload
			}
		}
		t.Fatal("could not build a payload near the cap")
		return nil
	}

	t.Run("a payload over the cap is the 422 rejection envelope", func(t *testing.T) {
		h, _, wsID, _, _ := newInternalPagesFixture(t, "fleet-201")
		body := pagesInternalBody(wsID, "sluzby",
			json.RawMessage(`{"blob":"`+strings.Repeat("x", pages.MaxPayloadBytes+2048)+`"}`))
		rr := pagesInternalPush(t, h, "fleet-201", body)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422; body: %s", rr.Code, rr.Body.String())
		}
		var rej struct {
			Rejected bool           `json:"rejected"`
			Kind     string         `json:"kind"`
			Message  string         `json:"message"`
			Detail   map[string]any `json:"detail"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &rej); err != nil {
			t.Fatalf("the refusal is not the rejection envelope: %v (%s)", err, rr.Body.String())
		}
		if !rej.Rejected || rej.Kind != "cap" {
			t.Errorf("envelope = %+v, want rejected=true kind=cap", rej)
		}
		if got, _ := rej.Detail["bytes_limit"].(float64); int(got) != pages.MaxPayloadBytes {
			t.Errorf("detail.bytes_limit = %v, want %d — the caller is owed the payload cap, "+
				"not this path's envelope bound", rej.Detail["bytes_limit"], pages.MaxPayloadBytes)
		}
	})

	t.Run("an unbounded envelope is refused before it is parsed", func(t *testing.T) {
		h, _, wsID, _, _ := newInternalPagesFixture(t, "fleet-201")
		// Far past the envelope bound, and deliberately not valid JSON past the
		// first field: nothing may parse this, it may only be refused.
		raw := []byte(`{"workspace_id":"` + wsID + `","junk":"` +
			strings.Repeat("x", pages.MaxPayloadBytes+internalPagePushEnvelopeSlack+4096) + `"}`)
		rr := pagesInternalPushRaw(t, h, "fleet-201", raw)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 — /api/v1/internal/* has no BodyCap in front of it, "+
				"so an unbounded body must be refused here; body: %s", rr.Code, rr.Body.String())
		}
		var rej struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &rej)
		if rej.Kind != "cap" {
			t.Errorf("kind = %q, want cap", rej.Kind)
		}
	})

	t.Run("a legal payload just under the cap is stored", func(t *testing.T) {
		h, _, wsID, _, _ := newInternalPagesFixture(t, "fleet-201")
		data := nearCap(t, pages.MaxPayloadBytes-1024)
		rr := pagesInternalPush(t, h, "fleet-201", pagesInternalBody(wsID, "tabulka", data))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — a payload inside the documented cap must not be "+
				"refused for travelling with its own workspace id; body: %s",
				rr.Code, truncateForTest(rr.Body.String()))
		}
	})
}

// ── 5. The envelope's own contract ─────────────────────────────────────────

// The rest of the request surface, table-driven. Each row is a shape the
// dispatcher or a routine author can produce, and each answer is the one a
// producer script branches on.
func TestPagesInternalPush_EnvelopeContract(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(body map[string]any)
		slug       string
		wantStatus int
		wantText   string
	}{
		{"no workspace_id", func(b map[string]any) { delete(b, "workspace_id") },
			"fleet-201", http.StatusBadRequest, "workspace_id"},
		{"no panel", func(b map[string]any) { delete(b, "panel") },
			"fleet-201", http.StatusBadRequest, "panel"},
		{"no data", func(b map[string]any) { delete(b, "data") },
			"fleet-201", http.StatusBadRequest, "data"},
		{"null data", func(b map[string]any) { b["data"] = nil },
			"fleet-201", http.StatusBadRequest, "data"},
		{"unknown panel", func(b map[string]any) { b["panel"] = "neznamy" },
			"fleet-201", http.StatusNotFound, "neznamy"},
		{"unknown page", nil, "neexistuje", http.StatusNotFound, "neexistuje"},
		{"another workspace", func(b map[string]any) { b["workspace_id"] = "ws-somebody-else" },
			"fleet-201", http.StatusNotFound, "fleet-201"},
		{"a state the producer may not claim", func(b map[string]any) { b["state"] = "fresh" },
			"fleet-201", http.StatusBadRequest, "ok"},
		{"a payload the schema refuses", func(b map[string]any) { b["data"] = map[string]any{"items": "not a list"} },
			"fleet-201", http.StatusBadRequest, "status.v1"},
		// The producer's own verdict IS its to claim (§4 rule 2).
		{"state=failed", func(b map[string]any) { b["state"] = "failed" },
			"fleet-201", http.StatusOK, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, wsID, _, _ := newInternalPagesFixture(t, "fleet-201")
			body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
			if tc.mutate != nil {
				tc.mutate(body)
			}
			rr := pagesInternalPush(t, h, tc.slug, body)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if tc.wantText != "" && !strings.Contains(rr.Body.String(), tc.wantText) {
				t.Errorf("the answer does not mention %q: %s", tc.wantText, rr.Body.String())
			}
		})
	}
}

// A page in another workspace is not reachable by naming it, and neither is a
// panel: the read is scoped by workspace_id before anything else happens. The
// row above covers the 404; this covers the half that matters more, which is
// that the OTHER tenant's page was not touched.
func TestPagesInternalPush_CannotReachAnotherTenantsPage(t *testing.T) {
	h, _, wsID, _, _ := newInternalPagesFixture(t, "fleet-201")
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
	body["workspace_id"] = "ws-other"
	if rr := pagesInternalPush(t, h, "fleet-201", body); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a page outside the named workspace; body: %s",
			rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_panel_data`).Scan(&n); err != nil {
		t.Fatalf("count payloads: %v", err)
	}
	if n != 0 {
		t.Errorf("%d payloads were written by a cross-tenant push", n)
	}
}

// truncateForTest keeps a failure message readable when the body under test is
// tens of kilobytes.
func truncateForTest(s string) string {
	if len(s) <= 400 {
		return s
	}
	return s[:400] + "… (truncated)"
}

// ── 6. The route is mounted, and behind the internal fence ─────────────────

// A handler nothing routes to is a handler that does not exist. This drives the
// real Router so the registration in router_internal.go is what is checked, and
// asserts the fence at the same time: an unauthenticated caller gets the
// prefix's uniform 404, not a 401 that would confirm the route is there
// (#1501).
func TestPagesInternalPush_RouteIsMountedBehindTheInternalFence(t *testing.T) {
	r, _ := newFenceRouter(t)

	probe := func(headers map[string]string, addr string) int {
		rr := probeInternalFence(t, r, "PUT", "/api/v1/internal/pages/fleet-201/data", addr, headers)
		return rr.Code
	}
	if got := probe(nil, fenceAttackerAddr); got != http.StatusNotFound {
		t.Errorf("an unauthenticated probe got %d; every rejection under the prefix is a uniform 404", got)
	}
	// With the token, the route is reached — 400 for the empty `{}` body the
	// probe sends, which only a mounted handler can produce.
	got := probe(map[string]string{"X-Internal-Token": fenceInternalToken}, fenceLoopbackAddr)
	if got == http.StatusNotFound || got == http.StatusMethodNotAllowed {
		t.Errorf("PUT /api/v1/internal/pages/{page}/data answered %d for an authorised caller — "+
			"the route is not mounted, so `page.write` would 404 at 03:00", got)
	}
}

// ── 7. Provenance cannot be forged from the body ───────────────────────────

// §4 rules 2 and 5: provenance is server-attached. The dispatcher injects
// author_run_id from the RUN and strips whatever the author wrote
// (crewship_actions.go crewshipInjected), but the handler must not be the place
// where a forged value would have worked anyway — an unresolvable run id stores
// NULL rather than a claim, and produced_at is this machine's clock.
func TestPagesInternalPush_ProvenanceIsNotTakenFromTheBody(t *testing.T) {
	h, _, wsID, _, pageID := newInternalPagesFixture(t, "fleet-201")

	// A crew grant, so authority does not depend on the run resolving.
	if _, err := h.db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, granted_by_user_id)
		VALUES (?, 'crew', ?, 'produce', 'test-user-id')`, pageID, pagesCrewID); err != nil {
		t.Fatalf("insert grant: %v", err)
	}

	body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
	body["author_run_id"] = "run_that_does_not_exist"
	body["produced_at"] = "1999-01-01T00:00:00Z" // a field this route does not have
	rr := pagesInternalPush(t, h, "fleet-201", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var runID sql.NullString
	var producedAt string
	if err := h.db.QueryRow(`
		SELECT d.producer_run_id, d.produced_at
		FROM page_panel_data d JOIN page_panels p ON p.id = d.panel_id
		WHERE p.panel_id = 'sluzby'`).Scan(&runID, &producedAt); err != nil {
		t.Fatalf("no stored payload: %v", err)
	}
	if runID.Valid {
		t.Errorf("producer_run_id = %q for a run that does not exist — provenance must point at a "+
			"run this workspace can open, or at nothing", runID.String)
	}
	if strings.HasPrefix(producedAt, "1999") {
		t.Errorf("produced_at = %q — the server's clock is the only clock (§4 rule 2)", producedAt)
	}
}
