package api

// Tests for PATCH /api/v1/agents/{agentId}/chats/{chatId} — the rename
// endpoint (PRD chat-as-a-primary-surface, Step 2).
//
// `chats.title` has existed since the first migration and nothing ever wrote
// it, so every session in every list reads "Untitled session". These tests pin
// the three things that make the write safe rather than merely present:
//
//   - what a title may contain (one line, no control characters, capped);
//   - who may set it (the same creator-or-agent-editor gate DeleteChat uses);
//   - that a chat is only reachable through its OWN agent and workspace.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// titleBody encodes a title the way a client would, so the test never has to
// hand-write JSON escapes for control characters (which are illegal raw).
func titleBody(t *testing.T, title string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		t.Fatalf("marshal title: %v", err)
	}
	return string(b)
}

// patchChatTitleReq drives UpdateChat directly, the way deleteChatReq drives
// DeleteChat: path values set by hand, workspace + user + role in the context
// (the router's middleware is what puts them there in production).
func patchChatTitleReq(t *testing.T, h *AgentHandler, userID, wsID, role, agentID, chatID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID+"/chats/"+chatID,
		strings.NewReader(body))
	r.SetPathValue("agentId", agentID)
	r.SetPathValue("chatId", chatID)
	r = withWorkspaceUser(r, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.UpdateChat(rr, r)
	return rr
}

// storedTitle reads the column back so a test asserts on what was persisted,
// not on what the handler echoed.
func storedTitle(t *testing.T, h *AgentHandler, chatID string) (string, bool) {
	t.Helper()
	var title *string
	if err := h.db.QueryRow(`SELECT title FROM chats WHERE id = ?`, chatID).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title == nil {
		return "", false
	}
	return *title, true
}

// Named code points, so the intent of each case is readable and no invisible
// character has to be pasted into a source line.
const (
	zwj    = "\u200D" // ZERO WIDTH JOINER — inside emoji sequences, must survive
	zwsp   = "\u200B" // ZERO WIDTH SPACE — invisible, must not
	rtlOvr = "\u202E" // RIGHT-TO-LEFT OVERRIDE — the filename-spoofing character
	bell   = "\x07"   // BEL, a C0 control character
	soh    = "\x01"   // SOH, another C0 control character
)

// ─── validation ──────────────────────────────────────────────────────────

func TestUpdateChat_TitleValidation(t *testing.T) {
	atCap := strings.Repeat("a", chatTitleMaxRunes)
	family := "\U0001F468" + zwj + "\U0001F469" + zwj + "\U0001F467"

	cases := []struct {
		name string
		// title is marshalled into a well-formed body; rawBody overrides it for
		// the cases that are about the body itself.
		title   string
		rawBody string
		want    int
		// stored is the exact value the column must hold on a 200.
		stored string
	}{
		{name: "empty", title: "", want: http.StatusBadRequest},
		{name: "whitespace only", title: "   \t  ", want: http.StatusBadRequest},
		{name: "newlines only", title: "\n\n", want: http.StatusBadRequest},
		{name: "control chars only", title: bell + soh, want: http.StatusBadRequest},
		{name: "zero width only", title: zwsp + zwsp, want: http.StatusBadRequest},
		{name: "key absent", rawBody: `{}`, want: http.StatusBadRequest},
		{name: "null title", rawBody: `{"title":null}`, want: http.StatusBadRequest},
		{name: "wrong type", rawBody: `{"title":42}`, want: http.StatusBadRequest},
		{name: "malformed json", rawBody: `{"title":`, want: http.StatusBadRequest},

		{name: "at the cap", title: atCap, want: http.StatusOK, stored: atCap},
		{name: "one over the cap", title: atCap + "a", want: http.StatusBadRequest},

		{name: "trims", title: "  Refactor the queue worker  ",
			want: http.StatusOK, stored: "Refactor the queue worker"},
		{name: "newline becomes one space", title: "first line\nsecond line",
			want: http.StatusOK, stored: "first line second line"},
		{name: "CRLF becomes one space", title: "first\r\nsecond",
			want: http.StatusOK, stored: "first second"},
		{name: "tabs and runs collapse", title: "a\t\t  b",
			want: http.StatusOK, stored: "a b"},
		{name: "control character stripped", title: "bell" + bell + "ring",
			want: http.StatusOK, stored: "bellring"},
		{name: "bidi override stripped", title: "safe" + rtlOvr + "txt.exe",
			want: http.StatusOK, stored: "safetxt.exe"},
		{name: "zero width space stripped", title: "a" + zwsp + "b",
			want: http.StatusOK, stored: "ab"},
		// A ZWJ emoji sequence must survive the format-character strip, or
		// every family emoji in a title silently becomes three people.
		{name: "zwj emoji sequence survives", title: "family " + family,
			want: http.StatusOK, stored: "family " + family},
		// Markup is stored verbatim: the title is user content and escaping
		// belongs to whatever renders it. Mangling it here would both corrupt
		// legitimate titles ("<Draft> plan") and hide the real requirement.
		{name: "markup stored verbatim", title: "<script>alert(1)</script>",
			want: http.StatusOK, stored: "<script>alert(1)</script>"},
		{name: "ampersand stored verbatim", title: "Tom & Jerry",
			want: http.StatusOK, stored: "Tom & Jerry"},
		{name: "quote stored verbatim", title: `He said "no"`,
			want: http.StatusOK, stored: `He said "no"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := covAUHandler(t)
			seedChatForDelete(t, h, wsID, "agent-rn", "chat-rn", userID)

			body := tc.rawBody
			if body == "" {
				body = titleBody(t, tc.title)
			}
			rr := patchChatTitleReq(t, h, userID, wsID, "MEMBER", "agent-rn", "chat-rn", body)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, tc.want, rr.Body.String())
			}
			got, ok := storedTitle(t, h, "chat-rn")
			if tc.want != http.StatusOK {
				if ok {
					t.Fatalf("rejected rename must not write the column, got %q", got)
				}
				return
			}
			if !ok || got != tc.stored {
				t.Fatalf("stored title = %q (set=%v), want %q", got, ok, tc.stored)
			}
		})
	}
}

// TestUpdateChat_CapIsRunesNotBytes pins the cap's unit. A byte cap would give
// a Czech or Japanese author a title a quarter the length of an English one —
// chatTitleMaxRunes emoji is four times that in bytes and must be accepted,
// one more must not.
func TestUpdateChat_CapIsRunesNotBytes(t *testing.T) {
	atCap := strings.Repeat("😀", chatTitleMaxRunes)
	if utf8.RuneCountInString(atCap) != chatTitleMaxRunes || len(atCap) <= chatTitleMaxRunes {
		t.Fatalf("precondition: %d runes / %d bytes", utf8.RuneCountInString(atCap), len(atCap))
	}

	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-rb", "chat-rb", userID)

	rr := patchChatTitleReq(t, h, userID, wsID, "MEMBER", "agent-rb", "chat-rb", titleBody(t, atCap))
	if rr.Code != http.StatusOK {
		t.Fatalf("%d-rune emoji title: status = %d, want 200 — the cap must count runes, not bytes (body: %s)",
			chatTitleMaxRunes, rr.Code, rr.Body.String())
	}
	if got, _ := storedTitle(t, h, "chat-rb"); got != atCap {
		t.Fatalf("stored title truncated mid-sequence: %q", got)
	}

	rr = patchChatTitleReq(t, h, userID, wsID, "MEMBER", "agent-rb", "chat-rb", titleBody(t, atCap+"😀"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("%d-rune emoji title: status = %d, want 400", chatTitleMaxRunes+1, rr.Code)
	}
}

// ─── tenancy ─────────────────────────────────────────────────────────────

// TestUpdateChat_WrongAgent404 — a real chat id addressed through another
// agent's path must 404, exactly as MarkChatRead and DeleteChat answer. The
// nesting is part of the identity, not decoration.
func TestUpdateChat_WrongAgent404(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-own", "chat-own", userID)
	seedChatForDelete(t, h, wsID, "agent-other", "chat-other", userID)

	rr := patchChatTitleReq(t, h, userID, wsID, "OWNER", "agent-other", "chat-own",
		titleBody(t, "stolen"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if got, ok := storedTitle(t, h, "chat-own"); ok {
		t.Fatalf("title written through the wrong agent: %q", got)
	}
}

// TestUpdateChat_CrossWorkspace404 — the workspace comes from the request
// context and never from the body, and a chat in another tenant is
// indistinguishable from a missing one (proxy_attachments.go's convention).
func TestUpdateChat_CrossWorkspace404(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-ws", "chat-ws", userID)

	otherWS := "other-workspace-id"
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-other', ?, ?, 'OWNER')`,
		otherWS, userID); err != nil {
		t.Fatalf("seed other membership: %v", err)
	}

	rr := patchChatTitleReq(t, h, userID, otherWS, "OWNER", "agent-ws", "chat-ws",
		titleBody(t, "cross tenant"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if got, ok := storedTitle(t, h, "chat-ws"); ok {
		t.Fatalf("cross-tenant rename persisted: %q", got)
	}
}

// ─── authorization ───────────────────────────────────────────────────────

// TestUpdateChat_NonCreatorMemberForbidden mirrors DeleteChat's gate: a MEMBER
// who did not create the chat cannot rename it. Rename is reversible, but it
// still rewrites a label every other member of the workspace reads.
func TestUpdateChat_NonCreatorMemberForbidden(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-gate", "chat-gate", "someone-else")

	rr := patchChatTitleReq(t, h, userID, wsID, "MEMBER", "agent-gate", "chat-gate",
		titleBody(t, "mine now"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}
	if got, ok := storedTitle(t, h, "chat-gate"); ok {
		t.Fatalf("forbidden rename persisted: %q", got)
	}
}

// TestUpdateChat_AgentEditorRenamesAny — the second arm of the same gate: an
// ADMIN (canEditAgent) may rename any chat of that agent.
func TestUpdateChat_AgentEditorRenamesAny(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-adm", "chat-adm", "someone-else")

	rr := patchChatTitleReq(t, h, userID, wsID, "ADMIN", "agent-adm", "chat-adm",
		titleBody(t, "Tidied up"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if got, _ := storedTitle(t, h, "chat-adm"); got != "Tidied up" {
		t.Fatalf("stored title = %q", got)
	}
}

// ─── happy path + response contract ──────────────────────────────────────

// TestUpdateChat_PersistsAndListShowsIt is the acceptance case: the frontend
// renames, then the sidebar's own endpoint reports the new name. It also pins
// the published contract — the PATCH response is the SAME JSON shape one
// element of the list is, so the client can splice it in without a refetch.
func TestUpdateChat_PersistsAndListShowsIt(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-hp", "chat-hp", userID)

	rr := patchChatTitleReq(t, h, userID, wsID, "MEMBER", "agent-hp", "chat-hp",
		titleBody(t, "Refactor the queue worker"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	var updated map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode PATCH response: %v (body %s)", err, rr.Body.String())
	}
	if updated["title"] != "Refactor the queue worker" {
		t.Fatalf("PATCH response title = %v", updated["title"])
	}
	if updated["id"] != "chat-hp" || updated["agent_id"] != "agent-hp" {
		t.Fatalf("PATCH response identifies the wrong row: %#v", updated)
	}

	// The list endpoint — the sidebar's source — must agree.
	lr := httptest.NewRequest("GET", "/api/v1/agents/agent-hp/chats", nil)
	lr.SetPathValue("agentId", "agent-hp")
	lr = withWorkspaceUser(lr, userID, wsID, "MEMBER")
	lrr := httptest.NewRecorder()
	h.ListChats(lrr, lr)
	if lrr.Code != http.StatusOK {
		t.Fatalf("list status = %d", lrr.Code)
	}
	var listed []map[string]any
	if err := json.Unmarshal(lrr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("list returned %d rows, want 1", len(listed))
	}
	if listed[0]["title"] != "Refactor the queue worker" {
		t.Fatalf("list title = %v, want the renamed value", listed[0]["title"])
	}

	// Same key set, or the frontend cannot treat the two interchangeably.
	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	got, want := strings.Join(keys(updated), ","), strings.Join(keys(listed[0]), ",")
	if got != want {
		t.Fatalf("PATCH response shape = [%s], list element shape = [%s] — they must be identical", got, want)
	}
}

// TestUpdateChat_DoesNotReorderTheList locks that a rename is not activity:
// last_activity_at must survive untouched, or renaming an old thread would
// jump it to the top of the sidebar.
func TestUpdateChat_DoesNotReorderTheList(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedChatForDelete(t, h, wsID, "agent-ord", "chat-ord", userID)
	const activity = "2020-01-01T00:00:00.000Z"
	if _, err := h.db.Exec(`UPDATE chats SET last_activity_at = ? WHERE id = 'chat-ord'`, activity); err != nil {
		t.Fatalf("seed activity: %v", err)
	}

	rr := patchChatTitleReq(t, h, userID, wsID, "MEMBER", "agent-ord", "chat-ord",
		titleBody(t, "Old thread"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got string
	if err := h.db.QueryRow(`SELECT last_activity_at FROM chats WHERE id = 'chat-ord'`).Scan(&got); err != nil {
		t.Fatalf("read last_activity_at: %v", err)
	}
	if got != activity {
		t.Fatalf("last_activity_at = %q, want it untouched (%q) — a rename is not activity", got, activity)
	}
}

// ─── route registration ──────────────────────────────────────────────────

// TestUpdateChatRouteIsScopeExemptAndWorkspaceGated pins the wrapper choice.
// The route sits under authedMut(roleSelf) like create / read / delete: it
// needs RequireWorkspace (the handler reads the workspace from the context,
// and authedSelfMut would leave it empty), and it must be scope-exempt so a
// narrowly-scoped CLI token can title the sessions it creates — the handler's
// creator-or-editor gate is the real authorization.
func TestUpdateChatRouteIsScopeExemptAndWorkspaceGated(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	const pattern = "/api/v1/agents/{agentId}/chats/{chatId}"

	var found bool
	for _, mr := range r.mutationRoutes {
		if mr.Method == "PATCH" && mr.Pattern == pattern {
			found = true
			if mr.Scope != scopeSelf {
				t.Errorf("PATCH chat scope = %q, want scopeSelf (symmetry with create/read/delete)", mr.Scope)
			}
			if mr.Role != roleSelf {
				t.Errorf("PATCH chat role = %q, want roleSelf", mr.Role)
			}
		}
	}
	if !found {
		t.Fatalf("PATCH %s is not registered", pattern)
	}

	// And it is actually routable: an unauthenticated call must be rejected by
	// the auth chain (401), not answered 405 because no such method exists.
	req := httptest.NewRequest("PATCH", "/api/v1/agents/a1/chats/c1", strings.NewReader(`{"title":"x"}`))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code == http.StatusMethodNotAllowed || rr.Code == http.StatusNotFound {
		t.Fatalf("PATCH is not routed: status = %d", rr.Code)
	}
}
