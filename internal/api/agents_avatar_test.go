package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Persisted agent avatars (#1297).
//
// An agent's avatar is generated client-side by DiceBear from
// (avatar_seed, avatar_style). That makes the rendered face a function of
// the *installed library version*, so a dependency bump repaints every
// existing agent — see lib/__tests__/agent-avatar-stability.test.ts for
// the 9→10 spike that produced zero identical outputs across 10 styles.
// These tests pin the server side of the fix: the rendered SVG is stored
// once and served back verbatim, so the face survives an upgrade.

// A minimal but realistic stand-in for DiceBear output: only tags and
// attributes that actually appear in the generated collections (verified
// by scanning all 10 styles).
const testAvatarSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 120" ` +
	`shape-rendering="auto" width="128" height="128">` +
	`<mask id="m"><rect width="120" height="120" rx="0" fill="#fff"/></mask>` +
	`<g mask="url(#m)"><rect width="120" height="120" fill="#e78276"/>` +
	`<circle cx="60" cy="55" r="20" fill="#fff" fill-opacity="0.8"/>` +
	`<path d="M20 90h80" stroke="#000" stroke-width="2" stroke-linecap="round"/></g></svg>`

func newAvatarAgentEnv(t *testing.T) (*AgentHandler, string, string) {
	t.Helper()
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedAgentForStatus(t, h, "ag-av", wsID, "", "IDLE", false)
	return h, userID, wsID
}

func putAvatar(t *testing.T, h *AgentHandler, userID, wsID, agentID, role, svg string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"svg": svg})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("PUT", "/api/v1/agents/"+agentID+"/avatar", strings.NewReader(string(body)))
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.PutAvatar(rr, req)
	return rr
}

func getAvatar(t *testing.T, h *AgentHandler, userID, wsID, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/avatar", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.ServeAvatar(rr, req)
	return rr
}

// ---- validation ----------------------------------------------------------

func TestValidateAgentAvatarSVG_AcceptsGeneratorOutput(t *testing.T) {
	if err := validateAgentAvatarSVG(testAvatarSVG); err != nil {
		t.Fatalf("realistic DiceBear-shaped SVG rejected: %v", err)
	}
}

// The validator is an allowlist over the tag/attribute vocabulary the
// generator actually emits. Every case below is something that vocabulary
// does not contain and that could load or execute something in a context
// where the SVG is fetched directly rather than through <img>.
func TestValidateAgentAvatarSVG_RejectsActiveContent(t *testing.T) {
	cases := map[string]string{
		"script tag":      `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"event handler":   `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><rect width="1" height="1"/></svg>`,
		"foreignObject":   `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><body/></foreignObject></svg>`,
		"external image":  `<svg xmlns="http://www.w3.org/2000/svg"><image href="https://evil.test/x.png"/></svg>`,
		"use href":        `<svg xmlns="http://www.w3.org/2000/svg"><use href="https://evil.test/x#y"/></svg>`,
		"anchor":          `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect/></a></svg>`,
		"animate":         `<svg xmlns="http://www.w3.org/2000/svg"><animate attributeName="x" to="1"/></svg>`,
		"iframe":          `<svg xmlns="http://www.w3.org/2000/svg"><iframe src="x"/></svg>`,
		"xlink href":      `<svg xmlns="http://www.w3.org/2000/svg"><rect xlink:href="https://evil.test"/></svg>`,
		"not svg at all":  `<html><body>hi</body></html>`,
		"html after svg":  testAvatarSVG + `<script>alert(1)</script>`,
		"empty":           ``,
		"whitespace only": "   \n\t ",
	}
	for name, svg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateAgentAvatarSVG(svg); err == nil {
				t.Errorf("validator accepted %s", name)
			}
		})
	}
}

func TestValidateAgentAvatarSVG_RejectsOversize(t *testing.T) {
	// Largest style measured (notionists) is ~20 KB; the cap sits well
	// above that, so only something pathological trips it.
	huge := `<svg xmlns="http://www.w3.org/2000/svg"><path d="` +
		strings.Repeat("M0 0L1 1", maxAgentAvatarBytes) + `"/></svg>`
	if err := validateAgentAvatarSVG(huge); err == nil {
		t.Fatal("validator accepted an oversized SVG")
	}
}

// ---- store + serve -------------------------------------------------------

func TestPutAvatar_StoresAndServesVerbatim(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)

	rr := putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var put struct {
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &put); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if !strings.HasPrefix(put.AvatarURL, "/api/v1/agents/ag-av/avatar?v=") {
		t.Errorf("avatar_url = %q, want the serve endpoint with a cache buster", put.AvatarURL)
	}

	got := getAvatar(t, h, userID, wsID, "ag-av")
	if got.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200; body=%s", got.Code, got.Body.String())
	}
	// Verbatim: the bytes we serve must be the bytes the generator
	// produced, or the whole point (a stable face) is lost.
	if got.Body.String() != testAvatarSVG {
		t.Error("served SVG differs from the stored SVG")
	}
}

// Serving SVG from our own origin is the one new exposure this feature
// adds: <img> never executes SVG script, but a direct navigation to the
// URL would. The sandbox CSP is what closes that, so pin the headers.
func TestServeAvatar_SecurityHeaders(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)

	rr := getAvatar(t, h, userID, wsID, "ag-av")
	want := map[string]string{
		"Content-Type":            "image/svg+xml",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; sandbox",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want an immutable directive", cc)
	}
}

func TestServeAvatar_UnsetIs404(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	if rr := getAvatar(t, h, userID, wsID, "ag-av"); rr.Code != http.StatusNotFound {
		t.Errorf("GET with no stored avatar = %d, want 404", rr.Code)
	}
}

func TestPutAvatar_RejectsInvalidSVG(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	rr := putAvatar(t, h, userID, wsID, "ag-av", "ADMIN",
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT with active content = %d, want 400", rr.Code)
	}
	var stored int
	if err := h.db.QueryRow(`SELECT avatar_svg IS NOT NULL FROM agents WHERE id = 'ag-av'`).Scan(&stored); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if stored != 0 {
		t.Error("a rejected SVG was still written to the row")
	}
}

// Write-once. Backfill fills an empty slot; changing an existing face has
// to go through DELETE first. Without this, any caller with edit rights
// could silently swap a teammate's agent portrait on every page view.
func TestPutAvatar_DoesNotOverwriteExisting(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)

	other := strings.Replace(testAvatarSVG, "#e78276", "#00ff00", 1)
	rr := putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", other)
	if rr.Code != http.StatusConflict {
		t.Fatalf("second PUT = %d, want 409", rr.Code)
	}
	if got := getAvatar(t, h, userID, wsID, "ag-av"); got.Body.String() != testAvatarSVG {
		t.Error("a conflicting PUT overwrote the stored avatar")
	}
}

func TestPutAvatar_ViewerForbidden(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	if rr := putAvatar(t, h, userID, wsID, "ag-av", "VIEWER", testAvatarSVG); rr.Code != http.StatusForbidden {
		t.Errorf("VIEWER PUT = %d, want 403", rr.Code)
	}
}

// ---- invalidation --------------------------------------------------------

// The stored SVG is a render of (seed, style). Change either and the
// stored bytes no longer depict what the agent is configured to look
// like, so they must be dropped and re-derived.
func TestUpdateAgent_ClearsStoredAvatarOnSeedOrStyleChange(t *testing.T) {
	for _, field := range []string{"avatar_seed", "avatar_style"} {
		t.Run(field, func(t *testing.T) {
			h, userID, wsID := newAvatarAgentEnv(t)
			putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)

			body := `{"` + field + `":"thumbs"}`
			req := httptest.NewRequest("PATCH", "/api/v1/agents/ag-av", strings.NewReader(body))
			req.SetPathValue("agentId", "ag-av")
			req = withWorkspaceUser(req, userID, wsID, "ADMIN")
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("PATCH %s = %d, want 200; body=%s", field, rr.Code, rr.Body.String())
			}

			var stored int
			if err := h.db.QueryRow(`SELECT avatar_svg IS NOT NULL FROM agents WHERE id = 'ag-av'`).Scan(&stored); err != nil {
				t.Fatalf("readback: %v", err)
			}
			if stored != 0 {
				t.Errorf("changing %s left the stale rendered avatar in place", field)
			}
		})
	}
}

// A no-op patch on an unrelated field must NOT throw the render away —
// otherwise every rename re-triggers a backfill round-trip.
func TestUpdateAgent_KeepsStoredAvatarOnUnrelatedChange(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)

	req := httptest.NewRequest("PATCH", "/api/v1/agents/ag-av", strings.NewReader(`{"name":"Renamed"}`))
	req.SetPathValue("agentId", "ag-av")
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH name = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var stored int
	if err := h.db.QueryRow(`SELECT avatar_svg IS NOT NULL FROM agents WHERE id = 'ag-av'`).Scan(&stored); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if stored != 1 {
		t.Error("an unrelated rename discarded the stored avatar")
	}
}

func TestDeleteAvatar_ClearsAndFallsBackTo404(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-av/avatar", nil)
	req.SetPathValue("agentId", "ag-av")
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.DeleteAvatar(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
	if got := getAvatar(t, h, userID, wsID, "ag-av"); got.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE = %d, want 404", got.Code)
	}
}

// ---- read-path exposure --------------------------------------------------

// The list query must expose the avatar as a short URL, never as the
// inlined SVG: at a measured ~2.6 KB per default-style avatar, inlining
// would add ~500 KB to a 200-agent roster response.
func TestListAgents_ExposesAvatarURLNotInlineSVG(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)

	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("List = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "<svg") {
		t.Error("list response inlined the SVG payload")
	}
	if !strings.Contains(rr.Body.String(), `/api/v1/agents/ag-av/avatar?v=`) {
		t.Errorf("list response did not expose avatar_url; body=%s", rr.Body.String())
	}
}

func TestListAgents_AvatarURLNullWhenUnset(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)

	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	var got []struct {
		AvatarURL *string `json:"avatar_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(agents) = %d, want 1", len(got))
	}
	if got[0].AvatarURL != nil {
		t.Errorf("avatar_url = %q, want null so the client generates from the seed", *got[0].AvatarURL)
	}
}

// ---- generator contract --------------------------------------------------

// The validator is an allowlist derived from what DiceBear actually emits,
// which makes it only as correct as that derivation. If a collection uses a
// tag or attribute the allowlist is missing, PutAvatar starts rejecting
// legitimate avatars — and because the client falls back to generating from
// the seed, nothing visibly breaks: persistence would just silently never
// happen, which is the exact failure this whole feature exists to prevent.
//
// So pin the contract against real generator output. testdata is produced by
// scripts/gen-avatar-fixtures.mjs; regenerate it when @dicebear/* moves and
// this test tells you the vocabulary changed.
func TestValidateAgentAvatarSVG_AcceptsRealDiceBearOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "dicebear_avatars.json"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures map[string]string
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures — the generator contract is unpinned")
	}
	for name, svg := range fixtures {
		t.Run(name, func(t *testing.T) {
			if err := validateAgentAvatarSVG(svg); err != nil {
				t.Errorf("validator rejected real generator output: %v", err)
			}
			// Whatever we accept must also survive the round trip we
			// promise callers: stored verbatim, served byte-for-byte.
			if h := agentAvatarHash(svg); len(h) != 16 {
				t.Errorf("hash length = %d, want 16", len(h))
			}
		})
	}
}

// Every fixture must fit the storage cap with room to spare; if a future
// collection blows past it, persistence would silently stop for that style.
func TestRealDiceBearOutput_FitsStorageCap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "dicebear_avatars.json"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures map[string]string
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	for name, svg := range fixtures {
		if len(svg) > maxAgentAvatarBytes/2 {
			t.Errorf("%s is %d bytes, over half the %d-byte cap — raise the cap",
				name, len(svg), maxAgentAvatarBytes)
		}
	}
}

// ---- other read paths ----------------------------------------------------

// The inbox renders sender avatars from its own batched lookup rather than
// the agent list query, so it needs the stored render too. Without this the
// same agent would show its stored face on its card and a freshly generated
// (post-upgrade, therefore different) one in the inbox.
func TestInboxList_CarriesStoredAvatarURL(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	// One sender with a stored render, one without.
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, avatar_seed, avatar_style, avatar_svg, avatar_svg_hash)
		 VALUES ('ag-stored', ?, 'Stored', 'stored', 'seed-a', 'thumbs', ?, 'deadbeefdeadbeef')`,
		wsID, testAvatarSVG); err != nil {
		t.Fatalf("seed stored agent: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, avatar_seed, avatar_style)
		 VALUES ('ag-plain', ?, 'Plain', 'plain', 'seed-b', 'thumbs')`, wsID); err != nil {
		t.Fatalf("seed plain agent: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, a := range []struct{ id, agent string }{{"esc-stored", "ag-stored"}, {"esc-plain", "ag-plain"}} {
		if _, err := db.Exec(`
			INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, body_md,
				sender_type, sender_id, sender_name, state, priority, blocking, payload_json, created_at, updated_at)
			VALUES (?, ?, 'escalation', ?, 'T', '', 'agent', ?, 's', 'unread', 'high', 1, '{}', ?, ?)`,
			a.id, wsID, "src-"+a.id, a.agent, now, now); err != nil {
			t.Fatalf("seed inbox item: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/inbox", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp inboxListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]string{}
	for _, row := range resp.Rows {
		got[row.ID] = row.AvatarURL
	}
	// The workspace must ride along: the inbox is one of the four places
	// that builds this URL, and the route is gated by wsCtx, so a URL
	// without it 400s in the browser (#1307).
	if want := "/api/v1/agents/ag-stored/avatar?v=deadbeefdeadbeef&workspace_id=" + wsID; got["esc-stored"] != want {
		t.Errorf("stored sender avatar_url = %q, want %q", got["esc-stored"], want)
	}
	if got["esc-plain"] != "" {
		t.Errorf("sender with no stored render got avatar_url %q, want empty so the client generates", got["esc-plain"])
	}
}

// ---- parser-vs-scanner regression ----------------------------------------

// The first version of validateAgentAvatarSVG scanned with regexes and split
// tags on the first '>'. But '>' is legal inside an XML attribute value, so
//
//	<svg …><rect d="a>b" onload="alert(1)"/></svg>
//
// parked the handler in a region the scanner never examined — and it was
// accepted. The same shape hid any attribute at all, and comparing raw text
// meant `fill="&#x6a;avascript:…"` slipped past the scheme check too.
//
// The validator now walks a real XML token stream. These are the payloads
// that defeated the scanner; each must stay rejected. If someone ever
// "simplifies" this back to pattern matching, this test is the tripwire.
func TestValidateAgentAvatarSVG_ScannerBypassesStayClosed(t *testing.T) {
	cases := map[string]string{
		"'>' in an attribute value hides an event handler": `<svg xmlns="http://www.w3.org/2000/svg"><rect d="a>b" onload="alert(1)"/></svg>`,
		"'>' on the root element hides an onload":          `<svg xmlns="http://www.w3.org/2000/svg" width=">" onload="alert(document.domain)" height="1"></svg>`,
		"'>' in an attribute value hides an href":          `<svg xmlns="http://www.w3.org/2000/svg"><rect d="a>b" href="x"/></svg>`,
		"newline between attributes":                       "<svg xmlns=\"http://www.w3.org/2000/svg\"><rect d=\"a>b\"\nonload=\"alert(1)\"/></svg>",
		"entity-encoded javascript scheme":                 `<svg xmlns="http://www.w3.org/2000/svg"><rect fill="&#x6a;avascript:alert(1)"/></svg>`,
		"uppercase event handler":                          `<svg xmlns="http://www.w3.org/2000/svg"><rect ONLOAD="alert(1)"/></svg>`,
		"script smuggled in a foreign namespace":           `<svg xmlns="http://www.w3.org/2000/svg" xmlns:x="urn:x"><x:script>alert(1)</x:script></svg>`,
		"second root element appended":                     `<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg><svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`,
		"DOCTYPE with an entity definition":                `<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`,
		"external url() in a presentation attribute":       `<svg xmlns="http://www.w3.org/2000/svg"><rect fill="url(https://evil.test/x)"/></svg>`,
		"scheme-relative url() inside style":               `<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(//evil.test/x)"/></svg>`,
		"xlink:href in its own namespace":                  `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><rect xlink:href="https://evil.test"/></svg>`,
		"not well-formed":                                  `<svg xmlns="http://www.w3.org/2000/svg"><rect></svg>`,
		"root element is not <svg>":                        `<html xmlns="http://www.w3.org/1999/xhtml"><body/></html>`,
	}
	for name, svg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateAgentAvatarSVG(svg); err == nil {
				t.Errorf("validator accepted: %s", svg)
			}
		})
	}
}

// The flip side: tightening the validator must not start rejecting the
// perfectly ordinary constructs real avatars rely on. url(#id) is how every
// masked/gradient-filled collection references its own <defs>, and escaped
// text is legal content.
func TestValidateAgentAvatarSVG_KeepsAcceptingLegitimateConstructs(t *testing.T) {
	cases := map[string]string{
		"internal url(#id) reference": `<svg xmlns="http://www.w3.org/2000/svg"><mask id="m"><rect width="1" height="1"/></mask>` +
			`<g mask="url(#m)"><rect fill="#fff"/></g></svg>`,
		"escaped angle bracket in text": `<svg xmlns="http://www.w3.org/2000/svg"><desc>a &gt; b</desc><rect/></svg>`,
	}
	for name, svg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateAgentAvatarSVG(svg); err != nil {
				t.Errorf("validator rejected a legitimate construct: %v", err)
			}
		})
	}
}

// ---- invalidation: value-based, not presence-based ------------------------

// The agent settings page resubmits avatar_seed and avatar_style on every
// save, whether or not the user touched them. A presence-based check would
// therefore discard the stored render every time somebody renamed an agent
// or changed its model — silently undoing the feature for anyone who edits
// their agents, which is everyone.
func TestUpdateAgent_KeepsStoredAvatarWhenAvatarFieldsResubmittedUnchanged(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	if _, err := h.db.Exec(
		`UPDATE agents SET avatar_seed = 'seed-a', avatar_style = 'thumbs' WHERE id = 'ag-av'`); err != nil {
		t.Fatalf("seed avatar fields: %v", err)
	}
	putAvatar(t, h, userID, wsID, "ag-av", "ADMIN", testAvatarSVG)

	// Exactly what the settings page sends: the unrelated edit plus both
	// avatar fields at their current values.
	body := `{"name":"Renamed","avatar_seed":"seed-a","avatar_style":"thumbs"}`
	req := httptest.NewRequest("PATCH", "/api/v1/agents/ag-av", strings.NewReader(body))
	req.SetPathValue("agentId", "ag-av")
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var stored int
	if err := h.db.QueryRow(`SELECT avatar_svg IS NOT NULL FROM agents WHERE id = 'ag-av'`).Scan(&stored); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if stored != 1 {
		t.Error("resubmitting unchanged avatar fields discarded the stored render")
	}
}

// ---- tenancy -------------------------------------------------------------

// canEditAgent short-circuits to true for OWNER/ADMIN without checking the
// target agent's workspace, so isolation on these endpoints rests entirely
// on the `AND workspace_id = ?` predicate in each query. Pin that, the way
// the sibling webhook-rotate endpoint does.
func TestAgentAvatar_CrossWorkspaceIsolation(t *testing.T) {
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsA := seedTestWorkspace(t, h.db, userID)
	seedAgentForStatus(t, h, "ag-a", wsA, "", "IDLE", false)
	putAvatar(t, h, userID, wsA, "ag-a", "ADMIN", testAvatarSVG)

	// A second workspace the same user also owns — the caller is a genuine
	// ADMIN, just not in the agent's workspace. Inserted by hand because
	// seedTestWorkspace hard-codes one id/slug.
	const wsB = "test-workspace-b"
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Test B', 'test-b')`, wsB); err != nil {
		t.Fatalf("insert second workspace: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m2', ?, ?, 'OWNER')`,
		wsB, userID); err != nil {
		t.Fatalf("insert second membership: %v", err)
	}

	t.Run("GET", func(t *testing.T) {
		if rr := getAvatar(t, h, userID, wsB, "ag-a"); rr.Code != http.StatusNotFound {
			t.Errorf("cross-workspace GET = %d, want 404", rr.Code)
		}
	})

	t.Run("PUT", func(t *testing.T) {
		other := strings.Replace(testAvatarSVG, "#e78276", "#00ff00", 1)
		rr := putAvatar(t, h, userID, wsB, "ag-a", "ADMIN", other)
		if rr.Code != http.StatusNotFound {
			t.Errorf("cross-workspace PUT = %d, want 404", rr.Code)
		}
		var svg string
		if err := h.db.QueryRow(`SELECT avatar_svg FROM agents WHERE id = 'ag-a'`).Scan(&svg); err != nil {
			t.Fatalf("readback: %v", err)
		}
		if svg != testAvatarSVG {
			t.Error("cross-workspace PUT modified the agent's stored avatar")
		}
	})

	t.Run("DELETE", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-a/avatar", nil)
		req.SetPathValue("agentId", "ag-a")
		req = withWorkspaceUser(req, userID, wsB, "ADMIN")
		rr := httptest.NewRecorder()
		h.DeleteAvatar(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("cross-workspace DELETE = %d, want 404", rr.Code)
		}
		var stored int
		if err := h.db.QueryRow(`SELECT avatar_svg IS NOT NULL FROM agents WHERE id = 'ag-a'`).Scan(&stored); err != nil {
			t.Fatalf("readback: %v", err)
		}
		if stored != 1 {
			t.Error("cross-workspace DELETE cleared the agent's stored avatar")
		}
	})
}

// PutAvatar's zero-rows-affected branch has to tell "no such agent" from
// "already has one". Only the 409 side was covered.
func TestPutAvatar_UnknownAgentIs404(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	if rr := putAvatar(t, h, userID, wsID, "ag-does-not-exist", "ADMIN", testAvatarSVG); rr.Code != http.StatusNotFound {
		t.Errorf("PUT for an unknown agent = %d, want 404", rr.Code)
	}
}

func TestDeleteAvatar_UnknownAgentIs404(t *testing.T) {
	h, userID, wsID := newAvatarAgentEnv(t)
	req := httptest.NewRequest("DELETE", "/api/v1/agents/nope/avatar", nil)
	req.SetPathValue("agentId", "nope")
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.DeleteAvatar(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("DELETE for an unknown agent = %d, want 404", rr.Code)
	}
}

// ---- crew-level invalidation ---------------------------------------------

// A crew's avatar_style is the default for agents that haven't set their own,
// so both endpoints that can change it must drop the stored renders that
// depict the previous style. Without this the style change appears to do
// nothing for every agent that has already been backfilled.
func TestCrewStyleChange_ClearsStoredAvatars(t *testing.T) {
	// apply-avatar-style rewrites each agent's OWN style, so every agent in
	// the crew is affected.
	t.Run("apply-avatar-style clears all agents in the crew", func(t *testing.T) {
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		h := NewCrewHandler(db, newTestLogger())
		seedCrewRow(t, db, "crew-av", wsID, "Av", "av")
		seedAgentRow(t, db, "ag-1", wsID, "crew-av", "A1", "a1", "AGENT")
		seedAgentRow(t, db, "ag-2", wsID, "crew-av", "A2", "a2", "AGENT")
		if _, err := db.Exec(
			`UPDATE agents SET avatar_svg = ?, avatar_svg_hash = 'h' WHERE crew_id = 'crew-av'`,
			testAvatarSVG); err != nil {
			t.Fatalf("seed stored avatars: %v", err)
		}

		req := httptest.NewRequest("POST", "/api/v1/crews/crew-av/apply-avatar-style",
			strings.NewReader(`{"avatar_style":"adventurer"}`))
		req.SetPathValue("crewId", "crew-av")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.ApplyAvatarStyle(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("apply-avatar-style = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}

		var remaining int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM agents WHERE crew_id = 'crew-av' AND avatar_svg IS NOT NULL`).Scan(&remaining); err != nil {
			t.Fatalf("readback: %v", err)
		}
		if remaining != 0 {
			t.Errorf("%d agent(s) kept a render of the previous style", remaining)
		}
	})

	// PATCH /crews/{id} changes only the crew default, so it must clear the
	// renders of agents that INHERIT it — and leave alone the ones that
	// override it, whose faces this field does not affect.
	t.Run("crew PATCH clears inheriting agents only", func(t *testing.T) {
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		h := NewCrewHandler(db, newTestLogger())
		seedCrewRow(t, db, "crew-av", wsID, "Av", "av")
		seedAgentRow(t, db, "ag-inherit", wsID, "crew-av", "A1", "a1", "AGENT")
		seedAgentRow(t, db, "ag-override", wsID, "crew-av", "A2", "a2", "AGENT")
		if _, err := db.Exec(
			`UPDATE agents SET avatar_svg = ?, avatar_svg_hash = 'h' WHERE crew_id = 'crew-av'`,
			testAvatarSVG); err != nil {
			t.Fatalf("seed stored avatars: %v", err)
		}
		if _, err := db.Exec(
			`UPDATE agents SET avatar_style = 'thumbs' WHERE id = 'ag-override'`); err != nil {
			t.Fatalf("seed override: %v", err)
		}

		req := httptest.NewRequest("PATCH", "/api/v1/crews/crew-av",
			strings.NewReader(`{"avatar_style":"adventurer"}`))
		req.SetPathValue("crewId", "crew-av")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("crew PATCH = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}

		var inheritKept, overrideKept int
		if err := db.QueryRow(
			`SELECT avatar_svg IS NOT NULL FROM agents WHERE id = 'ag-inherit'`).Scan(&inheritKept); err != nil {
			t.Fatalf("readback inherit: %v", err)
		}
		if err := db.QueryRow(
			`SELECT avatar_svg IS NOT NULL FROM agents WHERE id = 'ag-override'`).Scan(&overrideKept); err != nil {
			t.Fatalf("readback override: %v", err)
		}
		if inheritKept != 0 {
			t.Error("an agent inheriting the crew style kept a render of the old style")
		}
		if overrideKept != 1 {
			t.Error("an agent with its own style lost its render to an unrelated crew-default change")
		}
	})
}
