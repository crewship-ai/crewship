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
	if want := "/api/v1/agents/ag-stored/avatar?v=deadbeefdeadbeef"; got["esc-stored"] != want {
		t.Errorf("stored sender avatar_url = %q, want %q", got["esc-stored"], want)
	}
	if got["esc-plain"] != "" {
		t.Errorf("sender with no stored render got avatar_url %q, want empty so the client generates", got["esc-plain"])
	}
}
