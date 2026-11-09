package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// #5260 refinement: a panel whose agent producer the owner did not authorize
// says so in plain words, says who can fix it, and the OWNER has one direct
// call that authorizes the producer explicitly. Nobody else can make the
// panel green: not an admin, not an Edit grantee, not an agent.

func pagesAuthorizeProducer(t *testing.T, h *PageHandler, wsID, userID, role, panelID string, agent bool) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/pages/fleet-201/panels/" + panelID + "/producer/authorize"
	req := pagesGrantRequest(t, "POST", target, wsID, userID, role, "")
	if agent {
		req.Header.Set(actingAgentSlugHeader, pagesProducerAgentSlug)
	}
	req.SetPathValue("slug", "fleet-201")
	req.SetPathValue("panelId", panelID)
	rr := httptest.NewRecorder()
	h.AuthorizePanelProducer(rr, req)
	return rr
}

// pagesPanelWire reads one panel off the page document as a given viewer.
func pagesPanelWire(t *testing.T, h *PageHandler, wsID, userID, role, panelID string) map[string]any {
	t.Helper()
	req := pagesGrantRequest(t, "GET", "/api/v1/pages/fleet-201", wsID, userID, role, "")
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get page as %s: %d %s", userID, rr.Code, rr.Body.String())
	}
	var doc struct {
		Panels []map[string]any `json:"panels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	for _, p := range doc.Panels {
		if p["id"] == panelID {
			return p
		}
	}
	t.Fatalf("panel %s missing from the page", panelID)
	return nil
}

func TestOwnerAuthorizesAnUnauthorizedAgentProducer(t *testing.T) {
	h, wsID, ownerID, pageID := pagesProducerFixture(t)
	if rr := pagesPatchAs(t, h, wsID, ownerID, "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?")); rr.Code != http.StatusOK {
		t.Fatalf("owner names the producer: %d %s", rr.Code, rr.Body.String())
	}
	// As the migration leaves a designation the owner did not make.
	if _, err := h.db.Exec(`UPDATE page_panels SET producer_designated_by_user_id='admin' WHERE page_id=? AND panel_id='sluzby'`, pageID); err != nil {
		t.Fatalf("re-attribute the designation: %v", err)
	}
	if !pagesProducerRetired(t, h, pageID) {
		t.Fatal("precondition: the designation is retired")
	}

	// The panel explains itself, to the owner and to everyone else.
	for _, tc := range []struct {
		who, role    string
		canAuthorize bool
	}{{ownerID, "OWNER", true}, {"editor", "MEMBER", false}, {"admin", "ADMIN", false}} {
		p := pagesPanelWire(t, h, wsID, tc.who, tc.role, "sluzby")
		reason, _ := p["reason"].(string)
		if !strings.HasPrefix(reason, "Producer not authorized by the owner") || !strings.Contains(reason, "Only the page's owner") {
			t.Errorf("%s reads reason %q, want the plain-words state and who can fix it", tc.who, reason)
		}
		auth, _ := p["producer_authorization"].(map[string]any)
		if auth == nil {
			t.Fatalf("%s: no producer_authorization on the panel: %+v", tc.who, p)
		}
		if auth["producer"] != "agent/"+pagesProducerAgentSlug {
			t.Errorf("%s: producer_authorization.producer = %v", tc.who, auth["producer"])
		}
		if got, _ := auth["can_authorize"].(bool); got != tc.canAuthorize {
			t.Errorf("%s: can_authorize = %v, want %v", tc.who, got, tc.canAuthorize)
		}
	}
	if p := pagesPanelWire(t, h, wsID, ownerID, "OWNER", "zatizeni"); p["producer_authorization"] != nil {
		t.Errorf("a routine-produced panel carries producer_authorization: %+v", p)
	}

	// Nobody but the owner, and no agent, authorizes it.
	for _, tc := range []struct {
		name, who, role string
		agent           bool
	}{
		{"workspace admin", "admin", "ADMIN", false},
		{"Edit grantee", "editor", "MEMBER", false},
		{"an agent acting for the owner", ownerID, "OWNER", true},
	} {
		rr := pagesAuthorizeProducer(t, h, wsID, tc.who, tc.role, "sluzby", tc.agent)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "owner") {
			t.Fatalf("%s authorizing: %d %s, want 403 naming the owner", tc.name, rr.Code, rr.Body.String())
		}
		if !pagesProducerRetired(t, h, pageID) || pagesAgentPush(t, h, wsID) != http.StatusForbidden {
			t.Fatalf("%s's refused authorization changed the designation", tc.name)
		}
	}
	if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "zatizeni", false); rr.Code != http.StatusConflict {
		t.Fatalf("authorizing a routine producer: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "nope", false); rr.Code != http.StatusNotFound {
		t.Fatalf("authorizing a missing panel: %d %s, want 404", rr.Code, rr.Body.String())
	}

	// The owner's explicit authorization: a new designation, audited.
	rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner authorizes: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out["producer"] != "agent/"+pagesProducerAgentSlug || out["authorized"] != true {
		t.Fatalf("authorize response = %s (%v)", rr.Body.String(), err)
	}
	var designator string
	if err := h.db.QueryRow(`SELECT COALESCE(producer_designated_by_user_id,'') FROM page_panels WHERE page_id=? AND panel_id='sluzby'`, pageID).Scan(&designator); err != nil {
		t.Fatal(err)
	}
	if designator != ownerID || pagesProducerRetired(t, h, pageID) {
		t.Fatalf("after authorization: designator %q, retired %v", designator, pagesProducerRetired(t, h, pageID))
	}
	if n := pagesAuditCount(t, h, "page.agent_producer.authorized", pageID); n != 1 {
		t.Fatalf("authorization audits = %d, want 1", n)
	}
	if code := pagesAgentPush(t, h, wsID); code != http.StatusOK {
		t.Fatalf("the owner-authorized producer cannot push: %d", code)
	}
	p := pagesPanelWire(t, h, wsID, ownerID, "OWNER", "sluzby")
	if reason, _ := p["reason"].(string); p["producer_authorization"] != nil || strings.Contains(reason, "not authorized") {
		t.Fatalf("the panel still reads unauthorized after the owner authorized it: %+v", p)
	}
	// Idempotent: a second call changes and audits nothing.
	if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false); rr.Code != http.StatusOK {
		t.Fatalf("second authorize: %d %s", rr.Code, rr.Body.String())
	}
	if n := pagesAuditCount(t, h, "page.agent_producer.authorized", pageID); n != 1 {
		t.Fatalf("a no-op authorization audited again: %d", n)
	}
	// The authorization is the owner's: it ends with their ownership.
	if _, err := h.db.Exec(`UPDATE pages SET owner_user_id='editor' WHERE id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	if !pagesProducerRetired(t, h, pageID) || pagesAgentPush(t, h, wsID) != http.StatusForbidden {
		t.Fatal("the previous owner's authorization survived the transfer")
	}
}

// pagesUnauthorizedProducer leaves sluzby with an agent producer the owner
// did not name, as the migration would.
func pagesUnauthorizedProducer(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, wsID, ownerID, pageID := pagesProducerFixture(t)
	if rr := pagesPatchAs(t, h, wsID, ownerID, "OWNER", pagesPanelsWithProducer("agent/"+pagesProducerAgentSlug, "Jede to?")); rr.Code != http.StatusOK {
		t.Fatalf("owner names the producer: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := h.db.Exec(`UPDATE page_panels SET producer_designated_by_user_id='admin' WHERE page_id=? AND panel_id='sluzby'`, pageID); err != nil {
		t.Fatalf("re-attribute the designation: %v", err)
	}
	return h, wsID, ownerID, pageID
}

func TestAuthorizePanelProducerEdges(t *testing.T) {
	t.Run("a caller the page does not reach gets 404", func(t *testing.T) {
		h, wsID, _, pageID := pagesUnauthorizedProducer(t)
		pagesSeedUser(t, h, wsID, "stranger", "stranger@example.com", "MEMBER")
		if rr := pagesAuthorizeProducer(t, h, wsID, "stranger", "MEMBER", "sluzby", false); rr.Code != http.StatusNotFound {
			t.Fatalf("stranger: %d %s, want 404", rr.Code, rr.Body.String())
		}
		if rr := pagesAuthorizeProducer(t, h, wsID, "stranger", "MEMBER", "nope", false); rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "nope") {
			t.Fatalf("stranger on a missing panel: %d %s, want the page 404 that names no panel", rr.Code, rr.Body.String())
		}
		if !pagesProducerRetired(t, h, pageID) {
			t.Fatal("a refused call changed the designation")
		}
	})

	t.Run("a private artifact delegates nothing, and offers no button", func(t *testing.T) {
		h, wsID, ownerID, pageID := pagesUnauthorizedProducer(t)
		if _, err := h.db.Exec(`UPDATE pages SET private_audience=1 WHERE id=?`, pageID); err != nil {
			t.Fatal(err)
		}
		if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "private") {
			t.Fatalf("owner on a private artifact: %d %s, want 409", rr.Code, rr.Body.String())
		}
		if !pagesProducerRetired(t, h, pageID) {
			t.Fatal("a private artifact's agent producer designation became live")
		}
		// Refused either way; the push path may answer a private page 404.
		if code := pagesAgentPush(t, h, wsID); code == http.StatusOK || code == http.StatusAccepted {
			t.Fatalf("the agent pushed to a private artifact's panel: %d", code)
		}
		auth, _ := pagesPanelWire(t, h, wsID, ownerID, "OWNER", "sluzby")["producer_authorization"].(map[string]any)
		if auth == nil || auth["can_authorize"] != false {
			t.Fatalf("private artifact producer_authorization = %+v, want can_authorize false", auth)
		}
	})

	t.Run("an agent reading the page is never offered the button", func(t *testing.T) {
		h, wsID, ownerID, _ := pagesUnauthorizedProducer(t)
		req := pagesGrantRequest(t, "GET", "/api/v1/pages/fleet-201", wsID, ownerID, "OWNER", "")
		req.Header.Set(actingAgentSlugHeader, pagesProducerAgentSlug)
		req.SetPathValue("slug", "fleet-201")
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("agent read: %d %s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), `"can_authorize":true`) {
			t.Fatalf("an agent read offers can_authorize: %s", rr.Body.String())
		}
	})

	t.Run("the audit records what the designation was", func(t *testing.T) {
		h, wsID, ownerID, pageID := pagesUnauthorizedProducer(t)
		if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false); rr.Code != http.StatusOK {
			t.Fatalf("owner authorizes: %d %s", rr.Code, rr.Body.String())
		}
		var meta string
		if err := h.db.QueryRow(`SELECT metadata FROM audit_logs WHERE action='page.agent_producer.authorized' AND entity_id=?`, pageID).Scan(&meta); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(meta), &m); err != nil {
			t.Fatal(err)
		}
		if m["previously_retired"] != true || m["previous_designated_by_user_id"] != "admin" || m["producer"] != "agent/"+pagesProducerAgentSlug || m["panel_id"] != "sluzby" {
			t.Fatalf("audit metadata = %+v", m)
		}
		var by string
		if err := h.db.QueryRow(`SELECT COALESCE(user_id,'') FROM audit_logs WHERE action='page.agent_producer.authorized' AND entity_id=?`, pageID).Scan(&by); err != nil || by != ownerID {
			t.Fatalf("audit user = %q (%v), want the owner", by, err)
		}
		// The access change is on the chained journal too, in the same transaction.
		var chained int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND json_extract(payload,'$.action')='page.agent_producer.authorized' AND json_extract(payload,'$.entity_id')=?`, wsID, pageID).Scan(&chained); err != nil || chained != 1 {
			t.Fatalf("chained journal entries = %d (%v), want 1", chained, err)
		}
	})

	t.Run("a crew-owned page: its members authorize, nobody else, and a deleted crew nobody", func(t *testing.T) {
		h, wsID, ownerID, pageID := pagesUnauthorizedProducer(t)
		if _, err := h.db.Exec(`DELETE FROM crew_members WHERE crew_id=? AND user_id=?`, pagesCrewID, ownerID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(`UPDATE pages SET owner_user_id=NULL, owner_crew_id=? WHERE id=?`, pagesCrewID, pageID); err != nil {
			t.Fatalf("hand the page to the crew: %v", err)
		}
		if rr := pagesAuthorizeProducer(t, h, wsID, "admin", "ADMIN", "sluzby", false); rr.Code != http.StatusForbidden {
			t.Fatalf("non-member admin on a crew page: %d %s, want 403", rr.Code, rr.Body.String())
		}
		if rr := pagesAuthorizeProducer(t, h, wsID, "editor", "MEMBER", "sluzby", false); rr.Code != http.StatusOK {
			t.Fatalf("crew member authorizes: %d %s", rr.Code, rr.Body.String())
		}
		if pagesProducerRetired(t, h, pageID) || pagesAgentPush(t, h, wsID) != http.StatusOK {
			t.Fatal("the crew member's authorization is not live")
		}
		// Another member calling on a live designation re-attributes nothing.
		pagesSeedUser(t, h, wsID, "second", "second@example.com", "MEMBER")
		pagesSeedCrewMember(t, h, pagesCrewID, "second")
		rr := pagesAuthorizeProducer(t, h, wsID, "second", "MEMBER", "sluzby", false)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"changed":false`) {
			t.Fatalf("second member on a live designation: %d %s, want 200 changed:false", rr.Code, rr.Body.String())
		}
		var by string
		if err := h.db.QueryRow(`SELECT producer_designated_by_user_id FROM page_panels WHERE page_id=? AND panel_id='sluzby'`, pageID).Scan(&by); err != nil || by != "editor" {
			t.Fatalf("designator = %q (%v), want editor kept", by, err)
		}
		if _, err := h.db.Exec(`UPDATE crews SET deleted_at='2026-11-09T00:00:00Z' WHERE id=?`, pagesCrewID); err != nil {
			t.Fatal(err)
		}
		if rr := pagesAuthorizeProducer(t, h, wsID, "editor", "MEMBER", "sluzby", false); rr.Code == http.StatusOK {
			t.Fatalf("a member of a deleted owning crew authorized: %s", rr.Body.String())
		}
		if !pagesProducerRetired(t, h, pageID) {
			t.Fatal("the deleted crew's designation is still live")
		}
	})
}

// producerState is everything a refused authorization or a refused push must
// leave untouched: the designation, the audit trail, the panel's data and
// the page's revision.
type producerState struct {
	Designator, Retired     string
	Audits, Journal, Rows   int
	MaxSeq, PagesVersion    int64
	LatestPayload, LatestAt string
}

func pagesProducerState(t *testing.T, h *PageHandler, wsID, pageID string) producerState {
	t.Helper()
	var s producerState
	if err := h.db.QueryRow(`SELECT COALESCE(producer_designated_by_user_id,''), COALESCE(producer_retired_at,'') FROM page_panels WHERE page_id=? AND panel_id='sluzby'`, pageID).Scan(&s.Designator, &s.Retired); err != nil {
		t.Fatalf("designation: %v", err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE entity_id=? AND action LIKE 'page.agent_producer.%'`, pageID).Scan(&s.Audits); err != nil {
		t.Fatalf("audits: %v", err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND json_extract(payload,'$.action') LIKE 'page.agent_producer.%'`, wsID).Scan(&s.Journal); err != nil {
		t.Fatalf("journal: %v", err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(d.seq), COALESCE(MAX(d.seq),0),
		COALESCE((SELECT payload_json FROM page_panel_data x WHERE x.panel_id=pp.id ORDER BY seq DESC LIMIT 1),''),
		COALESCE((SELECT produced_at FROM page_panel_data x WHERE x.panel_id=pp.id ORDER BY seq DESC LIMIT 1),'')
		FROM page_panels pp LEFT JOIN page_panel_data d ON d.panel_id=pp.id WHERE pp.page_id=? AND pp.panel_id='sluzby'`, pageID).Scan(&s.Rows, &s.MaxSeq, &s.LatestPayload, &s.LatestAt); err != nil {
		t.Fatalf("panel data: %v", err)
	}
	if err := h.db.QueryRow(`SELECT pages_version FROM pages WHERE id=?`, pageID).Scan(&s.PagesVersion); err != nil {
		t.Fatalf("pages_version: %v", err)
	}
	return s
}

// The opposition's security question (#5260): after the owner's
// authorization was refused on a private artifact, did the agent's push go
// through? It did not. The push path loads the page with no person in
// context, and loadPage answers a private page to that caller as not found,
// so the push is refused with 404 before any producer rule is read. The
// same push on the page made ordinary is refused 403 by the producer rule.
// Nothing is written either way, and only an explicit owner authorization
// on an ordinary page lets the agent push.
func TestPrivateArtifactRefusedAuthorizationAdmitsNoPush(t *testing.T) {
	setTestEncryptionKey(t) // the chain verification at the end needs the key
	h, wsID, ownerID, pageID := pagesUnauthorizedProducer(t)

	// (1) Nothing else lets this agent, or its crew, push: no live agent or
	// crew produce/write grant, and the designation is a non-owner's, retired.
	var liveGrants int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id=? AND subject_type IN ('agent','crew') AND level IN ('produce','write') AND retired_at IS NULL`, pageID).Scan(&liveGrants); err != nil || liveGrants != 0 {
		t.Fatalf("live agent/crew produce or write grants = %d (%v), want 0", liveGrants, err)
	}
	before := pagesProducerState(t, h, wsID, pageID)
	if before.Designator != "admin" || before.Retired == "" || before.Rows != 0 {
		t.Fatalf("precondition: %+v, want a retired admin designation and no data", before)
	}

	if _, err := h.db.Exec(`UPDATE pages SET private_audience=1 WHERE id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	before = pagesProducerState(t, h, wsID, pageID)

	// (2) The refused authorization changes nothing.
	if rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false); rr.Code != http.StatusConflict {
		t.Fatalf("owner on a private artifact: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if got := pagesProducerState(t, h, wsID, pageID); got != before {
		t.Fatalf("a refused authorization changed state:\nbefore %+v\nafter  %+v", before, got)
	}

	// (3) The agent's push is refused and writes nothing.
	push := func() *httptest.ResponseRecorder {
		body := pagesInternalBody(wsID, "sluzby", pagesStatusData())
		body["author_run_id"] = ""
		body["agent_id"] = "agent-writer"
		return pagesInternalPush(t, h, "fleet-201", body)
	}
	if rr := push(); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "not found") {
		t.Fatalf("agent push on a private artifact: %d %s, want the 404 refusal", rr.Code, rr.Body.String())
	}
	if got := pagesProducerState(t, h, wsID, pageID); got != before {
		t.Fatalf("a refused push changed state:\nbefore %+v\nafter  %+v", before, got)
	}

	// The same page made ordinary: the producer rule itself refuses (403).
	if _, err := h.db.Exec(`UPDATE pages SET private_audience=0 WHERE id=?`, pageID); err != nil {
		t.Fatal(err)
	}
	before = pagesProducerState(t, h, wsID, pageID)
	if rr := push(); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "holds no produce grant covering the panel") {
		t.Fatalf("agent push with an unauthorized designation: %d %s, want the producer-rule 403", rr.Code, rr.Body.String())
	}
	if got := pagesProducerState(t, h, wsID, pageID); got != before {
		t.Fatalf("a refused push changed state:\nbefore %+v\nafter  %+v", before, got)
	}

	// (4) The owner's explicit authorization works, with one audit row and
	// one chained journal entry naming the owner, and the push lands.
	rr := pagesAuthorizeProducer(t, h, wsID, ownerID, "OWNER", "sluzby", false)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"changed":true`) {
		t.Fatalf("owner authorizes: %d %s", rr.Code, rr.Body.String())
	}
	after := pagesProducerState(t, h, wsID, pageID)
	if after.Designator != ownerID || after.Retired != "" || after.Audits != before.Audits+1 || after.Journal != before.Journal+1 || after.Rows != 0 {
		t.Fatalf("after authorization: %+v (before %+v)", after, before)
	}
	var actor, meta string
	if err := h.db.QueryRow(`SELECT actor_id, json_extract(payload,'$.metadata') FROM journal_entries WHERE workspace_id=? AND json_extract(payload,'$.action')='page.agent_producer.authorized' AND json_extract(payload,'$.entity_id')=?`, wsID, pageID).Scan(&actor, &meta); err != nil {
		t.Fatalf("chained journal entry: %v", err)
	}
	if actor != ownerID || !strings.Contains(meta, `"previously_retired":true`) || !strings.Contains(meta, `"previous_designated_by_user_id":"admin"`) {
		t.Fatalf("chained journal entry: actor %q metadata %s", actor, meta)
	}
	// "Chained": the entry has a seq and hashes, links onto the entry
	// before it, and the workspace chain verifies end to end.
	var seq int64
	var prev, hash string
	if err := h.db.QueryRow(`SELECT seq, COALESCE(prev_hash,''), COALESCE(entry_hash,'') FROM journal_entries WHERE workspace_id=? AND json_extract(payload,'$.action')='page.agent_producer.authorized' AND json_extract(payload,'$.entity_id')=?`, wsID, pageID).Scan(&seq, &prev, &hash); err != nil {
		t.Fatalf("chain fields: %v", err)
	}
	if seq <= 0 || prev == "" || hash == "" {
		t.Fatalf("journal entry is not chained: seq %d prev %q hash %q", seq, prev, hash)
	}
	if seq > 1 {
		var tip string
		if err := h.db.QueryRow(`SELECT COALESCE(entry_hash,'') FROM journal_entries WHERE workspace_id=? AND seq=?`, wsID, seq-1).Scan(&tip); err != nil || tip != prev {
			t.Fatalf("prev_hash %q does not link onto the previous tip %q (%v)", prev, tip, err)
		}
	}
	if res, err := journal.VerifyChain(t.Context(), h.db, wsID); err != nil || !res.OK {
		t.Fatalf("workspace journal chain does not verify: %+v %v", res, err)
	}
	if rr := push(); rr.Code != http.StatusOK {
		t.Fatalf("push after the owner's authorization: %d %s", rr.Code, rr.Body.String())
	}
	if got := pagesProducerState(t, h, wsID, pageID); got.Rows != 1 || got.MaxSeq != 1 {
		t.Fatalf("the authorized push did not land once: %+v", got)
	}
}
