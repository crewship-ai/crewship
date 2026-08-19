package api

// Pages — the handler contract (docs/prd/pages.md §4, §7, §10, §10b.3, §11b).
//
// The CLI's acceptance test (cmd/crewship/cmd_page_test.go) proves the client
// never SENDS provenance and repeats what it is told. This file proves the
// other half, which only the server can: that provenance is attached, that the
// cap is real, that a panel goes stale on its own, and that an unauthorised
// push is refused loudly rather than absorbed.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// ── Fixture ────────────────────────────────────────────────────────────────

// pagesFakeClock is the injected time source. Freshness is arithmetic against
// it, so the SLA boundary is tested by moving the clock rather than by
// sleeping — a test that sleeps for an SLA is a test nobody runs.
type pagesFakeClock struct{ now time.Time }

func (c *pagesFakeClock) Now() time.Time { return c.now }

func (c *pagesFakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// pagesJournalSpy records what was emitted, because "an ACL nobody can audit
// is not a security control" is a claim with a testable half.
type pagesJournalSpy struct{ entries []journal.Entry }

func (s *pagesJournalSpy) Emit(_ context.Context, e journal.Entry) (string, error) {
	s.entries = append(s.entries, e)
	return "jrn_test", nil
}

func (s *pagesJournalSpy) Flush(_ context.Context) error { return nil }

func (s *pagesJournalSpy) firstOfType(t journal.EntryType) *journal.Entry {
	for i := range s.entries {
		if s.entries[i].Type == t {
			return &s.entries[i]
		}
	}
	return nil
}

// newPagesFixture returns a handler over a migrated database holding one
// workspace, one OWNER user and the crew every panel in this file is owned by.
func newPagesFixture(t *testing.T) (*PageHandler, *pagesJournalSpy, *pagesFakeClock, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-lookout', ?, 'Lookout', 'lookout')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	spy := &pagesJournalSpy{}
	clock := &pagesFakeClock{now: time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)}
	h := NewPageHandler(db, nil, newTestLogger()).SetJournal(spy).SetClockForTesting(clock)
	return h, spy, clock, wsID, userID
}

// pagesSpecBody is the create body a CLI sends: the PARSED spec (§11b.2), SLA
// as an integer (§11b.3).
func pagesSpecBody(slug string) string {
	return `{
		"slug": "` + slug + `",
		"name": "Flotila .201",
		"panels": [{
			"id": "sluzby",
			"schema": "status.v1",
			"title": "Jede to?",
			"owner": "crew/lookout",
			"producer": "script/watch-services.sh",
			"sla_seconds": 30,
			"span": 8
		}]
	}`
}

const pagesStatusPayload = `{"items":[{"name":"api","state":"ok","label":"200 OK"}]}`

func pagesRequest(t *testing.T, method, target, wsID, userID, role, body string) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, target, reader)
	ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com"})
	ctx = withWorkspace(ctx, wsID, role)
	return req.WithContext(ctx)
}

func pagesCreate(t *testing.T, h *PageHandler, wsID, userID, slug string) map[string]any {
	t.Helper()
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesSpecBody(slug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page: status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	return doc
}

func pagesGet(t *testing.T, h *PageHandler, wsID, userID, role, slug string) map[string]any {
	t.Helper()
	req := pagesRequest(t, "GET", "/api/v1/pages/"+slug, wsID, userID, role, "")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get page: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("get response is not JSON: %v", err)
	}
	return doc
}

func pagesPush(t *testing.T, h *PageHandler, wsID, userID, role, slug, panelID, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, "PUT",
		fmt.Sprintf("/api/v1/pages/%s/panels/%s/data", slug, panelID), wsID, userID, role, payload)
	req.SetPathValue("slug", slug)
	req.SetPathValue("panelId", panelID)
	rr := httptest.NewRecorder()
	h.PushData(rr, req)
	return rr
}

// pagesPanel digs one panel out of a page document.
func pagesPanel(t *testing.T, doc map[string]any, panelID string) map[string]any {
	t.Helper()
	list, _ := doc["panels"].([]any)
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := obj["id"].(string); id == panelID {
			return obj
		}
		if id, _ := obj["panel_id"].(string); id == panelID {
			return obj
		}
	}
	t.Fatalf("panel %q not in document: %s", panelID, mustPagesJSON(t, doc))
	return nil
}

func mustPagesJSON(t *testing.T, v any) string {
	t.Helper()
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// ── 1. The spec round-trips, and an unfed panel says so ────────────────────

// TestPagesCreate_RoundTripsSpecAndReportsNeverProduced covers §11b decision 8:
// there are FOUR states, and the SERVER sends the fourth. The client must never
// have to infer "nothing was ever pushed" from an absent field — that is how
// two clients end up disagreeing about the same page.
func TestPagesCreate_RoundTripsSpecAndReportsNeverProduced(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	doc := pagesGet(t, h, wsID, userID, "OWNER", "fleet-201")
	if got, _ := doc["slug"].(string); got != "fleet-201" {
		t.Errorf("slug = %v, want fleet-201", doc["slug"])
	}
	panel := pagesPanel(t, doc, "sluzby")
	for field, want := range map[string]string{
		"schema":   "status.v1",
		"owner":    "crew/lookout",
		"producer": "script/watch-services.sh",
		"title":    "Jede to?",
	} {
		if got, _ := panel[field].(string); got != want {
			t.Errorf("panel %s = %v, want %q", field, panel[field], want)
		}
	}
	// §11b.3: sla_seconds, an integer, is what crosses the wire.
	if sla, _ := panel["sla_seconds"].(float64); sla != 30 {
		t.Errorf("panel sla_seconds = %v, want 30 (§11b decision 3)", panel["sla_seconds"])
	}
	if span, _ := panel["span"].(float64); span != 8 {
		t.Errorf("panel span = %v, want 8", panel["span"])
	}
	if state, _ := panel["state"].(string); state != string(pages.StateNeverProduced) {
		t.Errorf("panel state = %v, want never_produced — the server knows there is no "+
			"page_panel_data row and says so (§11b decision 8)", panel["state"])
	}
	if _, present := panel["provenance"]; present {
		t.Errorf("a panel nothing was ever pushed to carries provenance: %s", mustPagesJSON(t, panel))
	}
}

// TestPagesCreate_RefusesAnOwnerCrewThatDoesNotExist is the authoring gate's
// second half (§10b.1): "checks that every declared producer and owner
// resolves… It stops an agent saving a page that names a routine which does
// not exist."
func TestPagesCreate_RefusesAnOwnerCrewThatDoesNotExist(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	body := strings.Replace(pagesSpecBody("fleet-202"), "crew/lookout", "crew/nobody", 1)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an owner crew that does not exist; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "nobody") {
		t.Errorf("the refusal does not name the crew it could not resolve: %s", rr.Body.String())
	}
}

// TestPagesCreate_RequiresRoleOrPageCreateCapability pins the v109 layer: a
// MEMBER is refused, and the same MEMBER holding page.create is not.
func TestPagesCreate_RequiresRoleOrPageCreateCapability(t *testing.T) {
	h, _, _, wsID, _ := newPagesFixture(t)
	db := h.db

	for _, tc := range []struct {
		name         string
		userID       string
		capabilities string
		wantStatus   int
	}{
		{"member without the capability", "member-plain", `["chat"]`, http.StatusForbidden},
		{"member holding page.create", "member-author", `["chat","page.create"]`, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Member')`,
				tc.userID, tc.userID+"@example.com"); err != nil {
				t.Fatalf("insert user: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities)
				VALUES (?, ?, ?, 'MEMBER', ?)`, "wm-"+tc.userID, wsID, tc.userID, tc.capabilities); err != nil {
				t.Fatalf("insert membership: %v", err)
			}
			InvalidateCapabilityCache(wsID, tc.userID)

			req := pagesRequest(t, "POST", "/api/v1/pages", wsID, tc.userID, "MEMBER", pagesSpecBody("page-"+tc.userID))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

// TestPagesCreate_CrewOwnedPage — §7.1 rule 1: "A page has exactly one owner,
// and it is either a user or a crew… A crew-owned page is the natural home for
// a crew's own status board and needs no personal owner at all."
func TestPagesCreate_CrewOwnedPage(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)

	body := strings.Replace(pagesSpecBody("fleet-crew"), `"slug":`, `"owner": "crew/lookout", "slug":`, 1)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	if got, _ := doc["owner"].(string); got != "crew/lookout" {
		t.Errorf("owner = %v, want crew/lookout", doc["owner"])
	}
	// The XOR the schema enforces: exactly one owner column is set.
	var ownerUser, ownerCrew string
	if err := h.db.QueryRow(`SELECT COALESCE(owner_user_id, ''), COALESCE(owner_crew_id, '')
		FROM pages WHERE workspace_id = ? AND slug = 'fleet-crew'`, wsID).Scan(&ownerUser, &ownerCrew); err != nil {
		t.Fatalf("read owner columns: %v", err)
	}
	if ownerUser != "" || ownerCrew == "" {
		t.Errorf("owner columns = (%q, %q), want the crew alone (§10's XOR CHECK)", ownerUser, ownerCrew)
	}
}

// ── 2. Provenance is the server's ──────────────────────────────────────────

// TestPagesPush_AttachesProvenanceServerSide is §4 rule 5 and §7.1b: "Every
// panel footer carries provenance: producer, run id, timestamp. Server-attached,
// not producer-claimed" / "identity comes from the token, never from the
// request body".
//
// The proof is that the pushed bytes contain none of the three, and all three
// come back — the producer from the panel's own declaration, the timestamp from
// the injected clock (so it is demonstrably the SERVER's), and a run reference
// naming the push.
func TestPagesPush_AttachesProvenanceServerSide(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}
	for _, claimed := range []string{"producer", "run_id", "produced_at", "provenance"} {
		if strings.Contains(pagesStatusPayload, claimed) {
			t.Fatalf("fixture bug: the pushed payload mentions %q, so this test cannot prove the server attached it", claimed)
		}
	}

	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	prov, ok := panel["provenance"].(map[string]any)
	if !ok {
		t.Fatalf("panel carries no provenance object (§11b decision 4): %s", mustPagesJSON(t, panel))
	}
	if got, _ := prov["producer"].(string); got != "script/watch-services.sh" {
		t.Errorf("provenance producer = %v, want the panel's declared producer", prov["producer"])
	}
	if got, _ := prov["run_id"].(string); strings.TrimSpace(got) == "" {
		t.Errorf("provenance carries no run reference; §4 rule 5 puts one in every panel footer")
	}
	// The timestamp is the injected clock's, which no client could have sent.
	if got, _ := prov["produced_at"].(string); got != clock.now.Format(time.RFC3339) {
		t.Errorf("provenance produced_at = %v, want the SERVER's clock %q",
			prov["produced_at"], clock.now.Format(time.RFC3339))
	}
}

// TestPagesPush_PayloadCannotCarryIdentity is the belt to that braces: the
// panel schemas are closed, so a producer cannot smuggle an identity in beside
// its data even if it tries.
func TestPagesPush_PayloadCannotCarryIdentity(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	claiming := `{"items":[{"name":"api","state":"ok"}],"producer":"agent/evil","produced_at":"1999-01-01T00:00:00Z"}`
	rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", claiming)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — status.v1 is a closed schema and does not admit identity fields; body: %s",
			rr.Code, rr.Body.String())
	}
	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if _, present := panel["provenance"]; present {
		t.Errorf("the refused push still landed: %s", mustPagesJSON(t, panel))
	}
}

// ── 3. The 64 KiB cap ──────────────────────────────────────────────────────

// TestPagesPush_OverCapIsA422RejectionEnvelope — §10b.3 caps a payload at
// 64 KiB and §10 fixes how it is refused: MaxBytesReader → decode → the richer
// 422-plus-rejection-envelope shape, never a bare 400 and never a 500.
func TestPagesPush_OverCapIsA422RejectionEnvelope(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("seed push: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	oversize := `{"blob":"` + strings.Repeat("x", pages.MaxPayloadBytes+2048) + `"}`
	rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", oversize)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (issue #1937: \"a payload over the cap returns 422 with a "+
			"rejection envelope, not a 500\"); body: %s", rr.Code, rr.Body.String())
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
	if !strings.Contains(rej.Message, "64 KiB") {
		t.Errorf("the message does not say what the limit is: %q", rej.Message)
	}
	if got, _ := rej.Detail["bytes_limit"].(float64); int(got) != pages.MaxPayloadBytes {
		t.Errorf("detail.bytes_limit = %v, want %d", rej.Detail["bytes_limit"], pages.MaxPayloadBytes)
	}

	// A refused push must not have replaced the good one.
	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if !strings.Contains(mustPagesJSON(t, panel["data"]), "200 OK") {
		t.Errorf("the refused push destroyed the last good payload: %s", mustPagesJSON(t, panel))
	}
}

// ── 4. Only the declared producer may write ────────────────────────────────

// TestPagesPush_UnauthorisedIs403PlusJournalPlusNotification — §7.1b rule 3:
// "A produce attempt on a panel the caller does not hold returns 403, writes a
// journal entry, and notifies the page owner. It is equally likely to be a
// misconfiguration or an injection, and both deserve a human's attention on the
// first occurrence rather than the hundredth."
func TestPagesPush_UnauthorisedIs403PlusJournalPlusNotification(t *testing.T) {
	h, spy, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	db := h.db
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('intruder', 'intruder@example.com', 'Nobody')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-intruder', ?, 'intruder', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	rr := pagesPush(t, h, wsID, "intruder", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — only the declared producer may write a panel (§7.1 rule 4); body: %s",
			rr.Code, rr.Body.String())
	}

	entry := spy.firstOfType(EntryPageProduceDenied)
	if entry == nil {
		t.Fatalf("no %s journal entry; a refusal nobody can audit is not a security control", EntryPageProduceDenied)
	}
	if entry.ActorID != "intruder" {
		t.Errorf("journal actor = %q, want the caller", entry.ActorID)
	}
	if got, _ := entry.Payload["panel"].(string); got != "sluzby" {
		t.Errorf("journal payload panel = %v, want sluzby", entry.Payload["panel"])
	}

	var target, title string
	err := db.QueryRow(`SELECT COALESCE(target_user_id, ''), title FROM inbox_items
		WHERE workspace_id = ? AND source_id LIKE 'page-produce-denied:%'`, wsID).Scan(&target, &title)
	if err != nil {
		t.Fatalf("no notification for the page owner: %v", err)
	}
	if target != userID {
		t.Errorf("notification target = %q, want the page owner %q", target, userID)
	}
	if !strings.Contains(title, "sluzby") {
		t.Errorf("notification title does not name the panel: %q", title)
	}

	// And nothing was stored.
	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateNeverProduced) {
		t.Errorf("panel state = %v after a refused push, want never_produced", panel["state"])
	}
}

// TestPagesPush_ProduceGrantIsSufficient — the other side of the same rule: a
// MEMBER who holds `produce` on this panel may write it, and one who holds it
// on a DIFFERENT panel may not (§7.1b: "an agent granted produce on one panel
// cannot overwrite another agent's panel on the same page").
func TestPagesPush_ProduceGrantIsSufficient(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	doc := pagesCreate(t, h, wsID, userID, "fleet-201")
	pageID, _ := doc["id"].(string)

	db := h.db
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('producer-user', 'producer@example.com', 'Producer')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-producer', ?, 'producer-user', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO page_grants (page_id, subject_type, subject_id, level, panel_ids, granted_by_user_id)
		VALUES (?, 'user', 'producer-user', 'produce', ?, ?)`, pageID, `["jiny-panel"]`, userID); err != nil {
		t.Fatalf("insert grant: %v", err)
	}

	if rr := pagesPush(t, h, wsID, "producer-user", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusForbidden {
		t.Fatalf("a produce grant scoped to another panel let the push through: status = %d, body: %s",
			rr.Code, rr.Body.String())
	}

	if _, err := db.Exec(`UPDATE page_grants SET panel_ids = ? WHERE page_id = ? AND subject_id = 'producer-user'`,
		`["sluzby"]`, pageID); err != nil {
		t.Fatalf("widen grant: %v", err)
	}
	if rr := pagesPush(t, h, wsID, "producer-user", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("a produce grant covering this panel was refused: status = %d, body: %s", rr.Code, rr.Body.String())
	}
}

// ── 5. Freshness, computed server-side ─────────────────────────────────────

// TestPagesGet_StateFollowsTheClockNotTheProducer — §4 rule 2: three states
// "computed server-side, never by the producer", and the boundary is
// `age >= sla` → stale.
func TestPagesGet_StateFollowsTheClockNotTheProducer(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}

	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateFresh) {
		t.Fatalf("a just-pushed panel reads %v, want fresh", panel["state"])
	}

	// One nanosecond before the SLA is still fresh; at the SLA it is stale.
	clock.advance(30*time.Second - time.Nanosecond)
	panel = pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateFresh) {
		t.Errorf("state = %v one nanosecond before the SLA, want fresh", panel["state"])
	}
	clock.advance(time.Nanosecond)
	panel = pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateStale) {
		t.Errorf("state = %v at exactly the SLA, want stale (§4: the boundary is age >= sla)", panel["state"])
	}
	// The payload is still there — a stale panel renders its last value with
	// the age beside it, it does not blank (§4 rule 3).
	if !strings.Contains(mustPagesJSON(t, panel["data"]), "200 OK") {
		t.Errorf("a stale panel lost its last value: %s", mustPagesJSON(t, panel))
	}
}

// TestPagesPush_ExplicitFailureIsTheProducersOnlyVerdict — §4 rule 2: `ok` and
// `failed` are the producer's; `fresh` and `stale` are the server's arithmetic
// and are not storable at all.
func TestPagesPush_ExplicitFailureIsTheProducersOnlyVerdict(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	req := pagesRequest(t, "PUT", "/api/v1/pages/fleet-201/panels/sluzby/data?state=failed",
		wsID, userID, "OWNER", pagesStatusPayload)
	req.SetPathValue("slug", "fleet-201")
	req.SetPathValue("panelId", "sluzby")
	rr := httptest.NewRecorder()
	h.PushData(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("failure push: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateFailed) {
		t.Errorf("state = %v after an explicit failure push, want failed", panel["state"])
	}

	// "fresh" is not a verdict a producer may claim.
	req = pagesRequest(t, "PUT", "/api/v1/pages/fleet-201/panels/sluzby/data?state=fresh",
		wsID, userID, "OWNER", pagesStatusPayload)
	req.SetPathValue("slug", "fleet-201")
	req.SetPathValue("panelId", "sluzby")
	rr = httptest.NewRecorder()
	h.PushData(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d for state=fresh, want 400 — freshness is the server's arithmetic", rr.Code)
	}
}

// TestPagesGet_DeletedProducerFailsThePanelAndKeepsIt — §10b.4, "when the
// ground moves": "If its producer is deleted, its owning crew is removed, or
// the agent holding produce is dismissed, the panel switches to failed with a
// stated reason — 'producer routine x no longer exists' — and stays on the
// page. A page is a fixed structure… silently shrinking it would mean the page
// lies about what it is supposed to show."
func TestPagesGet_DeletedProducerFailsThePanelAndKeepsIt(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	db := h.db
	if _, err := db.Exec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('pl-watch', ?, 'watch-services', 'Watch', '{}', 'hash')`, wsID); err != nil {
		t.Fatalf("insert routine: %v", err)
	}
	body := strings.Replace(pagesSpecBody("fleet-routine"), `"script/watch-services.sh"`, `"routine/watch-services"`, 1)
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create with a routine producer: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	// The routine goes away under the page.
	if _, err := db.Exec(`UPDATE pipelines SET deleted_at = '2026-08-12T10:00:00Z' WHERE id = 'pl-watch'`); err != nil {
		t.Fatalf("soft-delete routine: %v", err)
	}

	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-routine"), "sluzby")
	if state, _ := panel["state"].(string); state != string(pages.StateFailed) {
		t.Errorf("state = %v after its producer was deleted, want failed (§10b.4)", panel["state"])
	}
	if reason, _ := panel["reason"].(string); !strings.Contains(reason, "watch-services") {
		t.Errorf("reason = %v, want it to name the producer that no longer exists", panel["reason"])
	}
}

// ── 6. The sealed placeholder and the index rollup ─────────────────────────

// TestPagesGet_ForeignCrewPanelIsSealed — §7.1 rule 2 with §11b decision 14's
// wire shape: the panel is not omitted, it is replaced by exactly
// {panel_id, span, sealed, owner_crew_name}, and everything else about it is
// gone.
func TestPagesGet_ForeignCrewPanelIsSealed(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}

	db := h.db
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('outsider', 'outsider@example.com', 'Outsider')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-outsider', ?, 'outsider', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	// The `read` grant is what puts the outsider in front of this page at all
	// (pageReachRule, pages_grants_authz.go) — membership of the workspace is
	// not reach. What it must NOT do is unseal crew/lookout's panel, which is
	// the whole of what this test asserts below.
	pagesGrant(t, h, wsID, userID, "fleet-201",
		`{"subject_type":"user","subject":"outsider@example.com","level":"read"}`)

	doc := pagesGet(t, h, wsID, "outsider", "MEMBER", "fleet-201")
	panels, _ := doc["panels"].([]any)
	if len(panels) != 1 {
		t.Fatalf("the page has %d panels for an outsider, want 1 sealed placeholder — the grid must "+
			"have the same shape for everyone (§2.3): %s", len(panels), mustPagesJSON(t, doc))
	}
	panel, _ := panels[0].(map[string]any)
	if sealed, _ := panel["sealed"].(bool); !sealed {
		t.Fatalf("panel is not sealed for a viewer outside its crew: %s", mustPagesJSON(t, panel))
	}
	if got, _ := panel["panel_id"].(string); got != "sluzby" {
		t.Errorf("sealed panel_id = %v, want sluzby", panel["panel_id"])
	}
	if got, _ := panel["span"].(float64); got != 8 {
		t.Errorf("sealed span = %v, want the declared 8 — the slot keeps its width", panel["span"])
	}
	if got, _ := panel["owner_crew_name"].(string); got != "Lookout" {
		t.Errorf("sealed owner_crew_name = %v, want Lookout", panel["owner_crew_name"])
	}
	for _, forbidden := range []string{"schema", "producer", "sla_seconds", "data", "provenance", "state", "owner"} {
		if _, present := panel[forbidden]; present {
			t.Errorf("the sealed placeholder leaks %q: %s", forbidden, mustPagesJSON(t, panel))
		}
	}
}

// TestPagesList_CarriesTheFreshnessRollup — §11b decision 15. Without it the
// overview band and the STATUS facet have nothing to count.
func TestPagesList_CarriesTheFreshnessRollup(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}
	producedAt := clock.now.Format(time.RFC3339)
	// Time passes, but not past the SLA: the row must still read fresh, and
	// last_produced_at must still be the moment the DATA arrived rather than
	// the moment the clock happens to be at.
	clock.advance(10 * time.Second)

	req := pagesRequest(t, "GET", "/api/v1/pages", wsID, userID, "OWNER", "")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("list is not a JSON array: %v (%s)", err, rr.Body.String())
	}
	if len(rows) != 1 {
		t.Fatalf("list returned %d rows, want 1", len(rows))
	}
	row := rows[0]
	states, ok := row["panel_states"].(map[string]any)
	if !ok {
		t.Fatalf("the index row carries no panel_states rollup (§11b decision 15): %s", mustPagesJSON(t, row))
	}
	for _, state := range []string{"fresh", "stale", "failed", "never_produced"} {
		if _, present := states[state]; !present {
			t.Errorf("panel_states is missing %q; all four are always sent, zeros included", state)
		}
	}
	if got, _ := states["fresh"].(float64); got != 1 {
		t.Errorf("panel_states.fresh = %v, want 1", states["fresh"])
	}
	if got, _ := row["panel_count"].(float64); got != 1 {
		t.Errorf("panel_count = %v, want 1", row["panel_count"])
	}
	if got, _ := row["last_produced_at"].(string); got != producedAt {
		t.Errorf("last_produced_at = %v, want the newest produced_at %q — not updated_at, which is the "+
			"spec's mtime (§11b decision 15)", row["last_produced_at"], producedAt)
	}
	if got, _ := row["state"].(string); got != string(pages.StateFresh) {
		t.Errorf("row state = %v, want the worst visible panel's state", row["state"])
	}
}

// TestPagesPush_RingIsBoundedByTheWriteThatGrowsIt — §10b.3: the payload ring
// keeps the newest 200 payloads, and the bound is applied in the same
// transaction as the push rather than by a sweep that might not run.
func TestPagesPush_RingIsBoundedByTheWriteThatGrowsIt(t *testing.T) {
	h, _, clock, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	var panelRowID string
	if err := h.db.QueryRow(`SELECT id FROM page_panels WHERE panel_id = 'sluzby'`).Scan(&panelRowID); err != nil {
		t.Fatalf("read panel row: %v", err)
	}
	// A full ring, written straight to the table so the test does not spend
	// 200 HTTP round trips proving something about one line of eviction.
	for seq := 1; seq <= pages.RingMaxPayloads; seq++ {
		at := clock.now.Add(-time.Duration(pages.RingMaxPayloads-seq) * time.Minute).Format(time.RFC3339)
		if _, err := h.db.Exec(`INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, state)
			VALUES (?, ?, ?, ?, 'ok')`, panelRowID, seq, pagesStatusPayload, at); err != nil {
			t.Fatalf("seed ring row %d: %v", seq, err)
		}
	}

	// The seeded ring's newest payload is stamped at exactly `now`, and §10b.3's
	// floor refuses a second payload inside the panel's minimum interval — so
	// move past it. This test is about the COUNT bound; the floor has its own
	// (pages_limits_test.go).
	clock.advance(pages.ConfiguredPushLimits().MinInterval())

	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var count int
	var oldest, newest int64
	if err := h.db.QueryRow(`SELECT COUNT(*), MIN(seq), MAX(seq) FROM page_panel_data WHERE panel_id = ?`, panelRowID).
		Scan(&count, &oldest, &newest); err != nil {
		t.Fatalf("count ring: %v", err)
	}
	if count != pages.RingMaxPayloads {
		t.Errorf("ring holds %d payloads after the 201st push, want %d", count, pages.RingMaxPayloads)
	}
	if oldest != 2 {
		t.Errorf("oldest surviving seq = %d, want 2 — the eviction drops from the old end", oldest)
	}
	if newest != int64(pages.RingMaxPayloads)+1 {
		t.Errorf("newest seq = %d, want %d", newest, pages.RingMaxPayloads+1)
	}
}

// ── 7. Update and delete ───────────────────────────────────────────────────

// TestPagesUpdate_KeepsAPanelsRingAcrossAnEdit — §10b.1: an edit that leaves a
// panel in place must not resurrect it as "never produced". Deleting and
// re-inserting the row would cascade page_panel_data away and the panel would
// come back saying something untrue.
func TestPagesUpdate_KeepsAPanelsRingAcrossAnEdit(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")
	if rr := pagesPush(t, h, wsID, userID, "OWNER", "fleet-201", "sluzby", pagesStatusPayload); rr.Code != http.StatusOK {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}

	body := strings.Replace(pagesSpecBody("fleet-201"), `"title": "Jede to?"`, `"title": "Bezi to?"`, 1)
	req := pagesRequest(t, "PATCH", "/api/v1/pages/fleet-201", wsID, userID, "OWNER", body)
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: status = %d, body: %s", rr.Code, rr.Body.String())
	}

	panel := pagesPanel(t, pagesGet(t, h, wsID, userID, "OWNER", "fleet-201"), "sluzby")
	if got, _ := panel["title"].(string); got != "Bezi to?" {
		t.Errorf("title = %v, want the edited one", panel["title"])
	}
	if state, _ := panel["state"].(string); state != string(pages.StateFresh) {
		t.Errorf("state = %v after an edit that kept the panel, want fresh — the payload ring survives "+
			"an edit (§10b.1)", panel["state"])
	}

	// And the edit is a version.
	var versions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_versions`).Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 2 {
		t.Errorf("page_versions holds %d rows after create+update, want 2 (§10b.1: every save is a version)", versions)
	}
}

// TestPagesDelete_RemovesThePageAndItsPanels — DELETE, then a clean miss.
func TestPagesDelete_RemovesThePageAndItsPanels(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	req := pagesRequest(t, "DELETE", "/api/v1/pages/fleet-201", wsID, userID, "OWNER", "")
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204, body: %s", rr.Code, rr.Body.String())
	}

	req = pagesRequest(t, "GET", "/api/v1/pages/fleet-201", wsID, userID, "OWNER", "")
	req.SetPathValue("slug", "fleet-201")
	rr = httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("get after delete: status = %d, want 404", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "fleet-201") {
		t.Errorf("the not-found body does not name the page: %s", rr.Body.String())
	}
	var panels int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_panels`).Scan(&panels); err != nil {
		t.Fatalf("count panels: %v", err)
	}
	if panels != 0 {
		t.Errorf("%d panel rows survived the page delete", panels)
	}
}
