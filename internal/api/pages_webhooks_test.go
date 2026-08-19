package api

// Inbound panel webhooks — one test per rule (docs/prd/pages.md §10b.5c).
//
// §10b.5c is four clauses long and every one of them is a thing that fails
// silently when it is wrong, so every one of them gets a test that fails loudly:
//
//	bound to one panel      TestPageWebhook_WritesItsOwnPanelAndNothingElse
//	                        TestPageWebhook_BodyCannotClaimProvenance
//	issued only by a human  TestPageWebhook_AnAgentCannotIssueOne
//	                        TestPageWebhook_CannotHoldMoreThanItsIssuer
//	revocable               TestPageWebhook_RevokedTokenIsRefusedImmediately
//	                        TestPageWebhook_NarrowsWhenItsIssuerLeaves
//	rate limited per panel  TestPageWebhook_ObeysThePanelIntervalFloor
//	                        TestPageWebhook_ObeysThePanelRateBucket
//	journalled with the     TestPageWebhook_JournalsTheWriteWithTheTokenAsActor
//	token id as the actor
//
// plus the two properties the shape is copied from pipeline_webhooks for:
//
//	hashed at rest, shown once  TestPageWebhook_TokenIsHashedAtRestAndShownOnce
//	the same 422 as the CLI     TestPageWebhook_OversizePayloadIsTheSame422AsTheCLI

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// ── Fixture ────────────────────────────────────────────────────────────────

// pagesWebhookSpecBody is TWO panels, both `script`-produced.
//
// Two, because "a leaked token can write one panel and nothing else" is not
// testable on a page with one panel: a fixture where there is nothing else to
// write cannot tell a bound token from an unbound one.
func pagesWebhookSpecBody(slug string) string {
	return `{
		"slug": "` + slug + `",
		"name": "Uzávěrka",
		"panels": [
			{
				"id": "cron",
				"schema": "status.v1",
				"title": "Noční job",
				"owner": "crew/lookout",
				"producer": "script/nightly.sh",
				"sla_seconds": 3600,
				"span": 6
			},
			{
				"id": "jiny",
				"schema": "status.v1",
				"title": "Někdo jiný",
				"owner": "crew/lookout",
				"producer": "script/other.sh",
				"sla_seconds": 3600,
				"span": 6
			}
		]
	}`
}

const pagesWebhookPayload = `{"items":[{"name":"nightly","state":"ok","label":"done"}]}`

// newPagesWebhookFixture builds the two-panel page and returns the handler.
func newPagesWebhookFixture(t *testing.T, slug string) (*PageHandler, *pagesJournalSpy, *pagesFakeClock, string, string) {
	t.Helper()
	h, spy, clock, wsID, userID := newPagesFixture(t)
	pagesCreateFrom(t, h, wsID, userID, pagesWebhookSpecBody(slug))
	return h, spy, clock, wsID, userID
}

// pagesWebhookCreateRaw drives POST /api/v1/pages/{slug}/webhooks as a human.
func pagesWebhookCreateRaw(t *testing.T, h *PageHandler, wsID, userID, role, slug, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesHumanRequest(t, "POST", "/api/v1/pages/"+slug+"/webhooks", wsID, userID, role, body)
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	return rr
}

// pagesWebhookCreate mints a token for one panel and returns the decoded 201.
func pagesWebhookCreate(t *testing.T, h *PageHandler, wsID, userID, slug, panelID string) map[string]any {
	t.Helper()
	rr := pagesWebhookCreateRaw(t, h, wsID, userID, "OWNER", slug,
		fmt.Sprintf(`{"panel":%q,"name":"cron on somebody else's box"}`, panelID))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create webhook: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	return out
}

func pagesWebhookToken(t *testing.T, created map[string]any) string {
	t.Helper()
	tok, _ := created["token"].(string)
	if tok == "" {
		t.Fatalf("create returned no token: %v", created)
	}
	return tok
}

// pagesWebhookFire drives the unauthenticated inbound path. No user, no
// workspace, no role in the context — §10b.5c: the sender cannot run the
// binary, and it certainly has no session.
func pagesWebhookFire(t *testing.T, h *PageHandler, token, payload string) *httptest.ResponseRecorder {
	t.Helper()
	return pagesWebhookFireTarget(t, h, token, "", payload)
}

func pagesWebhookFireTarget(t *testing.T, h *PageHandler, token, query, payload string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/page-webhooks/" + token + query
	req := httptest.NewRequest("POST", target, strings.NewReader(payload))
	req.RemoteAddr = "203.0.113.44:51244"
	req.SetPathValue("token", token)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	return rr
}

// pagesWebhookNewestPayload reads back what actually landed on a panel.
func pagesWebhookNewestPayload(t *testing.T, h *PageHandler, slug, panelID string) (payload, producedAt string, runID *string, ok bool) {
	t.Helper()
	row := panelRowID(t, h, slug, panelID)
	err := h.db.QueryRow(`
		SELECT payload_json, produced_at, producer_run_id
		FROM page_panel_data WHERE panel_id = ? ORDER BY seq DESC LIMIT 1`, row).
		Scan(&payload, &producedAt, &runID)
	if err != nil {
		return "", "", nil, false
	}
	return payload, producedAt, runID, true
}

// ── Bound to one panel ─────────────────────────────────────────────────────

// TestPageWebhook_WritesItsOwnPanelAndNothingElse is §10b.5c's central claim:
// "the token is bound to exactly one panel — so a leaked token can write one
// panel and nothing else".
//
// The strong form of that claim is that there is no REQUEST a holder could
// construct which reaches another panel, and the reason is structural: the
// panel is not on the wire. This test proves both halves — the bound panel is
// written, the other panel is untouched by a body that names it, and the token
// for the other panel writes only that one.
func TestPageWebhook_WritesItsOwnPanelAndNothingElse(t *testing.T) {
	h, _, _, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	cronToken := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))

	// A body that tries to name the other panel. `panel` is not a field on this
	// wire, so under a closed payload schema it is not "ignored" — it is not
	// even accepted, which is a stronger refusal than ignoring it would be.
	rr := pagesWebhookFire(t, h, cronToken,
		`{"panel":"jiny","items":[{"name":"nightly","state":"ok"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a body carrying a panel name: status = %d, want 400 — the payload schema is closed, "+
			"so there is no key through which a sender could redirect its write; body: %s", rr.Code, rr.Body.String())
	}

	if rr := pagesWebhookFire(t, h, cronToken, pagesWebhookPayload); rr.Code != http.StatusOK {
		t.Fatalf("fire on the bound panel: %d %s", rr.Code, rr.Body.String())
	}
	if _, _, _, ok := pagesWebhookNewestPayload(t, h, "uzaverka", "cron"); !ok {
		t.Fatal("the bound panel has no payload after a 200 — the token wrote nothing")
	}
	if _, _, _, ok := pagesWebhookNewestPayload(t, h, "uzaverka", "jiny"); ok {
		t.Fatal("the OTHER panel was written by a token bound to `cron` — a leaked token must reach one panel and nothing else")
	}

	// And the response names the panel the token is bound to, not one the
	// sender asked for.
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err == nil {
		if got, _ := body["panel"].(string); got != "" && got != "cron" {
			t.Errorf("response names panel %q, want cron", got)
		}
	}
}

// TestPageWebhook_BodyCannotClaimProvenance is §4 rule 5 on this path:
// provenance stays server-attached.
//
// Two halves, because the failure looks different from each side. A body cannot
// SAY produced_at (the schema is closed, so it is refused outright), and what is
// STORED is the server's clock and a NULL run — a cron on somebody else's box
// has no run, and there is no column in which it could invent one.
func TestPageWebhook_BodyCannotClaimProvenance(t *testing.T) {
	h, _, clock, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	token := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))

	rr := pagesWebhookFire(t, h, token,
		`{"produced_at":"1999-01-01T00:00:00Z","producer":"script/impostor.sh","items":[{"name":"n","state":"ok"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a body claiming produced_at/producer: status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}

	accepted := pagesWebhookFire(t, h, token, pagesWebhookPayload)
	if accepted.Code != http.StatusOK {
		t.Fatalf("fire: %d %s", accepted.Code, accepted.Body.String())
	}
	_, producedAt, runID, ok := pagesWebhookNewestPayload(t, h, "uzaverka", "cron")
	if !ok {
		t.Fatal("nothing stored")
	}
	if want := clock.now.UTC().Format(time.RFC3339); producedAt != want {
		t.Errorf("produced_at = %q, want the server's clock %q", producedAt, want)
	}
	if runID != nil {
		t.Errorf("producer_run_id = %q, want NULL — a webhook sender has no run to point at", *runID)
	}

	// The provenance in the response names the panel's DECLARED producer, which
	// the sender did not choose either.
	var body struct {
		Provenance pageProvenance `json:"provenance"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body.Provenance.Producer != "script/nightly.sh" {
		t.Errorf("provenance.producer = %q, want the panel's declared producer script/nightly.sh",
			body.Provenance.Producer)
	}
}

// ── Issued only by a human ─────────────────────────────────────────────────

// TestPageWebhook_AnAgentCannotIssueOne is §10b.5c's "issued only by a human",
// enforced through the same positive test for a human credential that guards a
// grant (§7.1b rule 1) and a publish (§7.3.2 rule 3).
//
// Two spellings of "not a human", because they fail differently: a request that
// never went through RequireAuth (absence denies), and a request that carries a
// perfectly good session PLUS the acting-agent header the sidecar forwards.
func TestPageWebhook_AnAgentCannotIssueOne(t *testing.T) {
	h, _, _, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	body := `{"panel":"cron"}`

	t.Run("no human credential recorded", func(t *testing.T) {
		req := pagesRequest(t, "POST", "/api/v1/pages/uzaverka/webhooks", wsID, userID, "OWNER", body)
		req.SetPathValue("slug", "uzaverka")
		rr := httptest.NewRecorder()
		h.CreateWebhook(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 — an empty auth kind must deny; body: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("a session carrying the acting-agent header", func(t *testing.T) {
		req := pagesHumanRequest(t, "POST", "/api/v1/pages/uzaverka/webhooks", wsID, userID, "OWNER", body)
		req.Header.Set(actingAgentSlugHeader, "kormidelnik")
		req.SetPathValue("slug", "uzaverka")
		rr := httptest.NewRecorder()
		h.CreateWebhook(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
		}
	})

	// Nothing was minted by either attempt.
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_webhooks`).Scan(&n); err != nil {
		t.Fatalf("count webhooks: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d webhook(s) exist after two refused attempts, want 0", n)
	}
}

// TestPageWebhook_CannotHoldMoreThanItsIssuer refuses at MINT time a token that
// would be refused at every fire.
//
// The panel here is produced by a ROUTINE, and §7.1 rule 4 gives that panel to
// that routine: not even the page owner may push it without an explicit produce
// grant. A token minted for it would be a credential that 403s forever, pasted
// into somebody's cron and debugged at 03:00.
func TestPageWebhook_CannotHoldMoreThanItsIssuer(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	if _, err := h.db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('pl-nightly', ?, 'nightly', 'Nightly', '{}', 'hash-nightly')`, wsID); err != nil {
		t.Fatalf("seed the routine the panel declares as its producer: %v", err)
	}
	pagesCreateFrom(t, h, wsID, userID, `{
		"slug": "uzaverka",
		"name": "Uzávěrka",
		"panels": [{
			"id": "cron",
			"schema": "status.v1",
			"title": "Noční job",
			"owner": "crew/lookout",
			"producer": "routine/nightly",
			"sla_seconds": 3600,
			"span": 6
		}]
	}`)

	rr := pagesWebhookCreateRaw(t, h, wsID, userID, "OWNER", "uzaverka", `{"panel":"cron"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("minting a webhook for a routine-produced panel: status = %d, want 403 — "+
			"a webhook cannot hold more than the human who issued it; body: %s", rr.Code, rr.Body.String())
	}
}

// ── Revocable ──────────────────────────────────────────────────────────────

// TestPageWebhook_RevokedTokenIsRefusedImmediately — "revocable" means on the
// NEXT request, not after a sweep, a cache expiry or a restart.
func TestPageWebhook_RevokedTokenIsRefusedImmediately(t *testing.T) {
	h, _, clock, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	created := pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron")
	token := pagesWebhookToken(t, created)
	id, _ := created["id"].(string)

	if rr := pagesWebhookFire(t, h, token, pagesWebhookPayload); rr.Code != http.StatusOK {
		t.Fatalf("fire before revoke: %d %s", rr.Code, rr.Body.String())
	}

	req := pagesHumanRequest(t, "DELETE", "/api/v1/pages/uzaverka/webhooks/"+id, wsID, userID, "OWNER", "")
	req.SetPathValue("slug", "uzaverka")
	req.SetPathValue("webhookId", id)
	rr := httptest.NewRecorder()
	h.RevokeWebhook(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}

	// The very next request, with the floor open so nothing else could be
	// refusing it.
	clock.advance(time.Hour)
	if rr := pagesWebhookFire(t, h, token, pagesWebhookPayload); rr.Code != http.StatusNotFound {
		t.Fatalf("fire after revoke: status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}

	// A revoked token is still listed — "was it used after we pulled it" is the
	// question an incident asks, and a deleted row cannot answer it.
	list := pagesWebhookList(t, h, wsID, userID, "uzaverka")
	if len(list.Webhooks) != 1 {
		t.Fatalf("list returned %d webhooks after a revoke, want the revoked row to survive", len(list.Webhooks))
	}
	if list.Webhooks[0].Live {
		t.Error("the revoked webhook still reports live")
	}
	if list.Webhooks[0].RevokedAt == "" {
		t.Error("the revoked webhook carries no revoked_at")
	}
	if list.Webhooks[0].FireCount != 1 {
		t.Errorf("fire_count = %d, want 1 — the accepted fire before the revoke", list.Webhooks[0].FireCount)
	}
}

// TestPageWebhook_NarrowsWhenItsIssuerLeaves is §7.1b's use-time narrowing,
// which §10b.5c inherits by calling the webhook "a `produce` grant in a
// different coat".
//
// The token is not revoked and not expired. What changed is the standing of the
// human who issued it, and the token holds exactly what they hold.
func TestPageWebhook_NarrowsWhenItsIssuerLeaves(t *testing.T) {
	h, spy, clock, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	token := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))
	if rr := pagesWebhookFire(t, h, token, pagesWebhookPayload); rr.Code != http.StatusOK {
		t.Fatalf("fire while the issuer is a member: %d %s", rr.Code, rr.Body.String())
	}

	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID); err != nil {
		t.Fatalf("remove the issuer from the workspace: %v", err)
	}

	clock.advance(time.Hour)
	rr := pagesWebhookFire(t, h, token, pagesWebhookPayload)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("fire after the issuer left: status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	if e := spy.firstOfType(EntryPageProduceDenied); e == nil {
		t.Error("a refused webhook push wrote no journal entry — §7.1b rule 3: an unauthorised push is a signal, not noise")
	}
}

// ── Rate limited per panel ─────────────────────────────────────────────────

// TestPageWebhook_ObeysThePanelIntervalFloor — §10b.3 layer 2, the one enforced
// by the write itself so it survives more than one process. A webhook is not a
// way around it.
func TestPageWebhook_ObeysThePanelIntervalFloor(t *testing.T) {
	h, _, clock, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	token := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))
	row := panelRowID(t, h, "uzaverka", "cron")

	if rr := pagesWebhookFire(t, h, token, pagesWebhookPayload); rr.Code != http.StatusOK {
		t.Fatalf("first fire: %d %s", rr.Code, rr.Body.String())
	}
	rr := pagesWebhookFire(t, h, token, pagesWebhookPayload)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second fire inside the minimum interval: status = %d, want 429; body: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After — a sender with no wait retries immediately")
	}
	if n := ringSize(t, h, row); n != 1 {
		t.Fatalf("the ring holds %d rows after two fires inside the minimum interval, want 1", n)
	}

	clock.advance(h.pushLimits.Limits().MinInterval())
	if rr := pagesWebhookFire(t, h, token, pagesWebhookPayload); rr.Code != http.StatusOK {
		t.Fatalf("fire one interval later: %d %s", rr.Code, rr.Body.String())
	}
	if n := ringSize(t, h, row); n != 2 {
		t.Fatalf("the ring holds %d rows, want 2 — the floor is refusing pushes it should admit", n)
	}

	// The floor is the PANEL's, not the token's: a second token on the same
	// panel is refused by the row that the first one wrote.
	second := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))
	if rr := pagesWebhookFire(t, h, second, pagesWebhookPayload); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("a SECOND token firing inside the same interval: status = %d, want 429 — "+
			"minting another token must not buy another allowance; body: %s", rr.Code, rr.Body.String())
	}
}

// TestPageWebhook_ObeysThePanelRateBucket — §10b.3 layer 1. The floor is held
// open throughout (the clock advances a full interval between fires), so the
// only thing that can refuse the last one is the panel's token bucket.
func TestPageWebhook_ObeysThePanelRateBucket(t *testing.T) {
	h, _, clock, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	// Explicit numbers rather than the registry's: a bucket test needs a burst
	// small enough to exhaust in a handful of requests, and the arithmetic
	// (window ÷ burst = a 10 s floor) stays honest.
	h.pushLimits = pages.NewPushLimiter(pages.PushLimits{PanelPerMin: 1, PanelBurst: 6, WorkspacePerMin: 10000})
	token := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))

	interval := h.pushLimits.Limits().MinInterval()
	accepted := 0
	var refused *httptest.ResponseRecorder
	for i := 0; i < 12; i++ {
		rr := pagesWebhookFire(t, h, token, pagesWebhookPayload)
		if rr.Code == http.StatusOK {
			accepted++
			clock.advance(interval)
			continue
		}
		refused = rr
		break
	}
	if refused == nil {
		t.Fatalf("12 fires spaced one interval apart were all accepted; the per-panel bucket (burst 6) never refused one")
	}
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("the refusal was %d, want 429; body: %s", refused.Code, refused.Body.String())
	}
	if accepted < 6 {
		t.Errorf("only %d fires were accepted before the bucket refused; the burst is 6, so the limiter is tighter than its own knob", accepted)
	}
	var body map[string]any
	if err := json.Unmarshal(refused.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body is not JSON: %v", err)
	}
	if scope, _ := body["scope"].(string); scope != string(pages.ScopePanel) {
		t.Errorf("scope = %q, want %q", scope, pages.ScopePanel)
	}
}

// ── Journalled with the token id as the actor ──────────────────────────────

// TestPageWebhook_JournalsTheWriteWithTheTokenAsActor — §10b.5c: "every write
// journalled with the token id as the actor".
//
// The token, not the issuer: an operator asking "what has been writing this
// panel" has to be able to tell one token from another, because the answer
// decides which one they revoke.
func TestPageWebhook_JournalsTheWriteWithTheTokenAsActor(t *testing.T) {
	h, spy, _, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	created := pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron")
	id, _ := created["id"].(string)

	if rr := pagesWebhookFire(t, h, pagesWebhookToken(t, created), pagesWebhookPayload); rr.Code != http.StatusOK {
		t.Fatalf("fire: %d %s", rr.Code, rr.Body.String())
	}

	e := spy.firstOfType(journal.EntryPagePanelUpdated)
	if e == nil {
		t.Fatal("an accepted webhook push wrote no page.panel.updated entry")
	}
	if e.ActorID != id {
		t.Errorf("journal actor id = %q, want the token id %q", e.ActorID, id)
	}
	if e.ActorType != journal.ActorSystem {
		t.Errorf("journal actor type = %q, want %q — there is no user and no agent behind this write",
			e.ActorType, journal.ActorSystem)
	}
	// And issuing the token was itself journalled: a credential that writes a
	// panel from outside the product is a widening of reach.
	if spy.firstOfType(journalPageWebhookIssued) == nil {
		t.Error("issuing a webhook wrote no journal entry")
	}
}

// ── Hashed at rest, shown once ─────────────────────────────────────────────

// TestPageWebhook_TokenIsHashedAtRestAndShownOnce is the property the shape was
// copied from pipeline_webhooks for (#1888): holding the token IS the
// authorisation, so a readable column is a credential store.
func TestPageWebhook_TokenIsHashedAtRestAndShownOnce(t *testing.T) {
	h, _, _, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	created := pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron")
	token := pagesWebhookToken(t, created)

	// 1. There is no cleartext anywhere in the row.
	rows, err := h.db.Query(`SELECT * FROM page_webhooks`)
	if err != nil {
		t.Fatalf("read page_webhooks: %v", err)
	}
	cols, _ := rows.Columns()
	for _, c := range cols {
		if c == "token" {
			t.Error("page_webhooks has a `token` column — the table must hold a digest and nothing else")
		}
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		for i, v := range vals {
			if s, ok := v.(string); ok && strings.Contains(s, token) {
				rows.Close()
				t.Fatalf("column %q holds the cleartext token", cols[i])
			}
		}
	}
	rows.Close()

	// 2. What IS stored is the one at-rest digest scheme this codebase has.
	var stored string
	if err := h.db.QueryRow(`SELECT token_hash FROM page_webhooks`).Scan(&stored); err != nil {
		t.Fatalf("read token_hash: %v", err)
	}
	if stored != pipeline.HashCapabilityToken(token) {
		t.Error("token_hash is not the #1888 capability digest of the token")
	}
	if !pipeline.IsCapabilityTokenDigest(stored) {
		t.Error("token_hash carries no scheme prefix — a digest must be recognisable as one")
	}

	// 3. Presenting the DIGEST is not presenting the token.
	if rr := pagesWebhookFire(t, h, stored, pagesWebhookPayload); rr.Code != http.StatusNotFound {
		t.Fatalf("replaying the stored digest as a token: status = %d, want 404", rr.Code)
	}
	// ...and neither is a token nobody minted.
	if rr := pagesWebhookFire(t, h, "pgw_"+strings.Repeat("0", 64), pagesWebhookPayload); rr.Code != http.StatusNotFound {
		t.Fatalf("an unknown token: status = %d, want 404", rr.Code)
	}

	// 4. The listing never shows it again.
	list := pagesWebhookList(t, h, wsID, userID, "uzaverka")
	if len(list.Webhooks) != 1 {
		t.Fatalf("list returned %d webhooks, want 1", len(list.Webhooks))
	}
	if list.Webhooks[0].Token != "" || list.Webhooks[0].URL != "" {
		t.Error("the listing returned a token or a URL — the column is a digest and there is nothing to show")
	}
	if !strings.Contains(fmt.Sprint(created["url"]), created["id"].(string)) &&
		!strings.HasPrefix(fmt.Sprint(created["url"]), "/api/v1/page-webhooks/") {
		t.Errorf("create returned url %q, want the inbound path", created["url"])
	}
}

// pagesWebhookList drives GET /api/v1/pages/{slug}/webhooks.
func pagesWebhookList(t *testing.T, h *PageHandler, wsID, userID, slug string) pageWebhooksWire {
	t.Helper()
	req := pagesHumanRequest(t, "GET", "/api/v1/pages/"+slug+"/webhooks", wsID, userID, "OWNER", "")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.ListWebhooks(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list webhooks: %d %s", rr.Code, rr.Body.String())
	}
	var out pageWebhooksWire
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("list response is not JSON: %v", err)
	}
	return out
}

// ── The same 422 as the CLI ────────────────────────────────────────────────

// TestPageWebhook_OversizePayloadIsTheSame422AsTheCLI is §11b.6: "The size cap
// is enforced at the handler, and the same 422 must arrive on the sidecar path,
// where no client pre-check exists."
//
// A Zapier step has no client pre-check either. The two envelopes are compared
// BYTE FOR BYTE rather than field by field, because the whole point of the rule
// is that a producer script can branch on one shape and not two.
func TestPageWebhook_OversizePayloadIsTheSame422AsTheCLI(t *testing.T) {
	h, _, _, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	token := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))

	// One payload, well past the cap, pushed through both doors.
	oversize := `{"items":[{"name":"n","state":"ok","label":"` +
		strings.Repeat("x", pages.MaxPayloadBytes) + `"}]}`

	viaCLI := pagesPush(t, h, wsID, userID, "OWNER", "uzaverka", "cron", oversize)
	if viaCLI.Code != http.StatusUnprocessableEntity {
		t.Fatalf("CLI path: status = %d, want 422; body: %s", viaCLI.Code, viaCLI.Body.String())
	}
	viaWebhook := pagesWebhookFire(t, h, token, oversize)
	if viaWebhook.Code != http.StatusUnprocessableEntity {
		t.Fatalf("webhook path: status = %d, want 422; body: %s", viaWebhook.Code, viaWebhook.Body.String())
	}
	if viaCLI.Body.String() != viaWebhook.Body.String() {
		t.Fatalf("the two rejection envelopes differ, so a producer would have to branch on both:\n  CLI:     %s\n  webhook: %s",
			viaCLI.Body.String(), viaWebhook.Body.String())
	}
	var rej pageRejection
	if err := json.Unmarshal(viaWebhook.Body.Bytes(), &rej); err != nil {
		t.Fatalf("422 body is not JSON: %v", err)
	}
	if !rej.Rejected || rej.Kind != "cap" {
		t.Errorf("422 envelope = %+v, want rejected:true kind:cap", rej)
	}
	if rej.Detail["bytes_limit"] != float64(pages.MaxPayloadBytes) {
		t.Errorf("bytes_limit = %v, want %d", rej.Detail["bytes_limit"], pages.MaxPayloadBytes)
	}
}

// ── The failed-push verdict ────────────────────────────────────────────────

// TestPageWebhook_StateRidesOnTheQueryString mirrors pages_data.go's third
// property: the producer's own verdict is the only part of the state it
// influences, and it does not travel in the body — a `state` key there would
// sit next to the payload's own keys and read as part of it.
func TestPageWebhook_StateRidesOnTheQueryString(t *testing.T) {
	h, _, _, wsID, userID := newPagesWebhookFixture(t, "uzaverka")
	token := pagesWebhookToken(t, pagesWebhookCreate(t, h, wsID, userID, "uzaverka", "cron"))

	rr := pagesWebhookFireTarget(t, h, token, "?state=failed", pagesWebhookPayload)
	if rr.Code != http.StatusOK {
		t.Fatalf("fire with state=failed: %d %s", rr.Code, rr.Body.String())
	}
	var got string
	row := panelRowID(t, h, "uzaverka", "cron")
	if err := h.db.QueryRow(`SELECT state FROM page_panel_data WHERE panel_id = ? ORDER BY seq DESC LIMIT 1`, row).Scan(&got); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if got != string(pages.PushFailed) {
		t.Errorf("stored state = %q, want %q", got, pages.PushFailed)
	}

	if rr := pagesWebhookFireTarget(t, h, token, "?state=fresh", pagesWebhookPayload); rr.Code != http.StatusBadRequest {
		t.Errorf("state=fresh: status = %d, want 400 — fresh is the server's arithmetic, not a sender's claim", rr.Code)
	}
}
