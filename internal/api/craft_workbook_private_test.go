package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// A Sheet opened from a file the person attached in their private chat with an agent is private to
// them from its first version (private_audience 1, no folder, owner the person), in the same
// transaction; a group chat, no chat, or a page that was not just made keeps its audience.
func TestWorkbookFromAPrivateChatIsPrivateToItsPerson(t *testing.T) {
	f, h, ctx := craftNativeHTTPFixture(t)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	mustExec(t, f.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('chat-agent',?,'Morgan','morgan')`, f.wsID)
	mustExec(t, f.db, `INSERT INTO chats(id,agent_id,workspace_id,created_by,visibility) VALUES('chat-own','chat-agent',?,?,'private')`, f.wsID, f.userID)
	mustExec(t, f.db, `INSERT INTO chats(id,agent_id,workspace_id,created_by,visibility) VALUES('chat-group','chat-agent',?,?,'group')`, f.wsID, f.userID)
	initialize := func(page, chat string, old bool) (int, int) {
		f.seedPage(t, page, page, page, f.userID, "")
		if old {
			mustExec(t, f.db, `UPDATE pages SET created_at='2026-01-01 00:00:00' WHERE id=?`, page)
		}
		req := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/initialize", workbookInitializeReader(craftTestWorkbook(t, "xlsx", page))).WithContext(ctx)
		req.SetPathValue("slug", "@"+page)
		req.Header.Set("Content-Type", "application/json")
		if chat != "" {
			req.Header.Set("X-Craft-Chat", chat)
		}
		w := httptest.NewRecorder()
		d.initialize(w, req)
		var private int
		if err := f.db.QueryRow(`SELECT private_audience FROM pages WHERE id=?`, page).Scan(&private); err != nil {
			t.Fatal(err)
		}
		return w.Code, private
	}
	if code, private := initialize("wb-own", "chat-own", false); code != 201 || private != 1 {
		t.Fatalf("own private chat: %d, private_audience %d", code, private)
	}
	// The person's private chat never stores a version into a page that is not private: a page that
	// could not be made private (here one not just made) is refused before anything is saved.
	if code, private := initialize("wb-old", "chat-own", true); code != 409 || private != 0 {
		t.Errorf("a page not just made, private chat: %d, private_audience %d, want 409 and 0", code, private)
	}
	var versions int
	if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='wb-old'`).Scan(&versions); err != nil || versions != 0 {
		t.Errorf("the refused page has %d versions (%v), want 0", versions, err)
	}
	for name, c := range map[string]struct {
		chat string
		old  bool
	}{"group chat": {"chat-group", false}, "no chat": {"", false}, "an old page without a chat": {"", true}} {
		page := "wb-" + strings.ReplaceAll(name, " ", "-")
		if code, private := initialize(page, c.chat, c.old); code != 201 || private != 0 {
			t.Errorf("%s: %d, private_audience %d", name, code, private)
		}
	}
}

// The browser's order: POST /pages, initialize from the file in the person's private chat (which makes
// the page private and so moves the workspace's authority revision), then lease/release with the
// initialize session. The release succeeds, so the person viewing the Sheet holds no edit lease.
func TestWorkbookFromAPrivateChatReleasesItsLeaseInTheBrowsersOrder(t *testing.T) {
	f, h, ctx := craftNativeHTTPFixture(t)
	cfg := mooring.NativeStoreConfig{Application: "gridcraft", Format: mooring.NativeFormatWorkbook, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: craftWorkbookValidate}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"gridcraft": {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
	mustExec(t, f.db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('chat-agent',?,'Morgan','morgan')`, f.wsID)
	mustExec(t, f.db, `INSERT INTO chats(id,agent_id,workspace_id,created_by,visibility) VALUES('chat-own','chat-agent',?,?,'private')`, f.wsID, f.userID)
	f.seedPage(t, "wb-view", "wb-view", "wb-view", f.userID, "")
	var before int64
	if err := f.db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id=?`, f.wsID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/pages/@wb-view/craft/initialize", workbookInitializeReader(craftTestWorkbook(t, "xlsx", "wb-view"))).WithContext(ctx)
	req.SetPathValue("slug", "@wb-view")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Craft-Chat", "chat-own")
	w := httptest.NewRecorder()
	d.initialize(w, req)
	var out craftNativeInitializeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 201 || err != nil || out.Token == "" {
		t.Fatalf("initialize: %d %s", w.Code, w.Body)
	}
	var after int64
	var private int
	if err := f.db.QueryRow(`SELECT (SELECT authority_revision FROM workspaces WHERE id=?),(SELECT private_audience FROM pages WHERE id='wb-view')`, f.wsID).Scan(&after, &private); err != nil {
		t.Fatal(err)
	}
	if private != 1 || after <= before {
		t.Fatalf("private_audience %d, authority revision %d → %d: the case under test did not happen", private, before, after)
	}
	if rel := craftNativeLeaseCall(h, ctx, out.Token, "@wb-view", "release", `{"generation":"`+out.Generation+`"}`); rel.Code < 200 || rel.Code > 299 {
		t.Fatalf("lease/release with the initialize session: %d %s", rel.Code, rel.Body)
	}
	var actor string
	if err := f.db.QueryRow(`SELECT actor_id FROM mooring_edit_leases WHERE page_id='wb-view'`).Scan(&actor); err != nil || actor != "" {
		t.Fatalf("the person still holds the lease after release: %q %v", actor, err)
	}
}
