package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type presenceRig struct {
	t      *testing.T
	router *Router
	ws     string
	tokens map[string]string
}

type presenceReply struct {
	Peers []documentPresencePeer `json:"peers"`
	Color int                    `json:"color"`
	TTL   int64                  `json:"ttl_ms"`
}

// presenceFixture: a full Router (auth, workspace, RBAC scope and the live
// document read authority), Docs enabled, an owner and two members, and two
// documents A and B owned by the owner.
func presenceFixture(t *testing.T) *presenceRig {
	t.Helper()
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	router, err := NewRouter(db, "synthetic-document-presence-router-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	router.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		t.Fatal("presence must never call the document engine")
		return nil, 500, nil
	}
	rig := &presenceRig{t: t, router: router, ws: ws, tokens: map[string]string{"owner": mintTokenFor(t, db, owner, "presence-owner")}}
	rig.tokens["editor"] = seedRoleMemberToken(t, db, ws, "presence-editor", "MEMBER", "presence-editor")
	rig.tokens["reader"] = seedRoleMemberToken(t, db, ws, "presence-reader", "MEMBER", "presence-reader")
	mustExec(t, db, `UPDATE users SET full_name='Erin Editor' WHERE id='presence-editor'`)
	mustExec(t, db, `UPDATE users SET full_name='Rory Reader' WHERE id='presence-reader'`)
	for _, page := range []string{"doc-a", "doc-b"} {
		mustExec(t, db, `INSERT INTO pages (id, workspace_id, slug, name, owner_user_id, spec_json) VALUES (?, ?, ?, ?, ?, '{}')`, page, ws, page, page, owner)
	}
	mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('doc-a','user','presence-editor','write',?),('doc-a','user','presence-reader','read',?)`, owner, owner)
	return rig
}

func (r *presenceRig) beat(who, slug, body string) (int, presenceReply) {
	r.t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/pages/"+slug+"/document/presence", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+r.tokens[who])
	req.Header.Set("X-Workspace-ID", r.ws)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.router.mux.ServeHTTP(w, req)
	var reply presenceReply
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			r.t.Fatal(err)
		}
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		r.t.Fatalf("presence reply cacheable: %q", got)
	}
	return w.Code, reply
}

func caret(client, anchor string) string {
	position := base64.StdEncoding.EncodeToString([]byte(anchor))
	return `{"client":"` + client + `","anchor":"` + position + `","head":"` + position + `"}`
}

func presenceNames(peers []documentPresencePeer) []string {
	out := []string{}
	for _, peer := range peers {
		out = append(out, peer.Name)
	}
	return out
}

func TestDocumentPresenceNamesComeFromTheServerAndCaretsAreRelayed(t *testing.T) {
	rig := presenceFixture(t)
	if code, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "owner-caret")); code != 200 || len(reply.Peers) != 0 {
		t.Fatalf("first heartbeat %d %+v", code, reply)
	}
	code, reply := rig.beat("editor", "doc-a", caret("editor-tab-1", "editor-caret"))
	if code != 200 || len(reply.Peers) != 1 {
		t.Fatalf("editor heartbeat %d %+v", code, reply)
	}
	peer := reply.Peers[0]
	if peer.Name != "Test User" || peer.UserID != "test-user-id" || peer.Anchor != base64.StdEncoding.EncodeToString([]byte("owner-caret")) {
		t.Fatalf("owner seen as %+v", peer)
	}
	if peer.Color == reply.Color {
		t.Fatalf("two people on one document share colour %d", peer.Color)
	}
	if reply.TTL <= 0 {
		t.Fatal("reply must state the presence lifetime")
	}
	// A client cannot name itself or pick a colour: unknown fields are refused.
	for _, forged := range []string{
		`{"client":"editor-tab-1","name":"The Owner"}`,
		`{"client":"editor-tab-1","color":3}`,
		`{"client":"editor-tab-1","user_id":"test-user-id"}`,
		`{"client":"x"}`,
		`{"client":"editor-tab-1","anchor":"not base64!"}`,
		`{"client":"editor-tab-1","anchor":"` + base64.StdEncoding.EncodeToString([]byte("a")) + `"}`,
		`{"client":"editor-tab-1","anchor":"` + base64.StdEncoding.EncodeToString(make([]byte, documentPresencePositionLimit+1)) + `","head":"AA=="}`,
	} {
		if code, _ := rig.beat("editor", "doc-a", forged); code != 400 {
			t.Errorf("forged heartbeat %s accepted with %d", forged, code)
		}
	}
	_, reply = rig.beat("owner", "doc-a", caret("owner-tab-1", "owner-caret"))
	if got := presenceNames(reply.Peers); len(got) != 1 || got[0] != "Erin Editor" {
		t.Fatalf("owner sees %v", got)
	}
}

func TestDocumentPresenceNeverCrossesDocuments(t *testing.T) {
	rig := presenceFixture(t)
	rig.beat("editor", "doc-a", caret("editor-tab-1", "editor-caret"))
	// The owner on B sees nobody from A.
	if code, reply := rig.beat("owner", "doc-b", caret("owner-tab-b", "b-caret")); code != 200 || len(reply.Peers) != 0 {
		t.Fatalf("doc B shows %d %+v", code, reply)
	}
	// Without a grant on B the editor cannot heartbeat there and reads nobody.
	if code, reply := rig.beat("editor", "doc-b", caret("editor-tab-b", "x")); code != 404 || len(reply.Peers) != 0 {
		t.Fatalf("editor without a grant on B got %d %+v", code, reply)
	}
	// And was not recorded on B by the refused attempt.
	if _, reply := rig.beat("owner", "doc-b", caret("owner-tab-b", "b-caret")); len(reply.Peers) != 0 {
		t.Fatalf("refused heartbeat left presence on B: %+v", reply.Peers)
	}
}

func TestDocumentPresenceRevokedReaderDisappearsAndCannotRead(t *testing.T) {
	rig := presenceFixture(t)
	rig.beat("owner", "doc-a", caret("owner-tab-1", "owner-caret"))
	if code, reply := rig.beat("reader", "doc-a", caret("reader-tab-1", "reader-caret")); code != 200 || len(reply.Peers) != 1 {
		t.Fatalf("reader heartbeat %d %+v", code, reply)
	}
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "owner-caret")); len(reply.Peers) != 1 || reply.Peers[0].Name != "Rory Reader" {
		t.Fatalf("owner does not see the reader: %+v", reply.Peers)
	}
	mustExec(t, rig.router.db, `DELETE FROM page_grants WHERE page_id='doc-a' AND subject_id='presence-reader'`)
	// The revoked reader's next heartbeat is refused and reveals nobody.
	if code, reply := rig.beat("reader", "doc-a", caret("reader-tab-1", "reader-caret")); code != 404 || len(reply.Peers) != 0 {
		t.Fatalf("revoked reader got %d %+v", code, reply)
	}
	// ...and that refusal removed every tab of theirs at once.
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "owner-caret")); len(reply.Peers) != 0 {
		t.Fatalf("revoked reader still listed: %+v", reply.Peers)
	}
	// A revoked reader whose tab stays silent is gone once the TTL passes, without any heartbeat of theirs.
	mustExec(t, rig.router.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('doc-a','user','presence-reader','read','test-user-id')`)
	rig.beat("reader", "doc-a", caret("reader-tab-2", "reader-caret"))
	mustExec(t, rig.router.db, `DELETE FROM page_grants WHERE page_id='doc-a' AND subject_id='presence-reader'`)
	base := time.Now()
	rig.router.documentPresenceHub.now = func() time.Time { return base.Add(documentPresenceTTL + time.Second) }
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "owner-caret")); len(reply.Peers) != 0 {
		t.Fatalf("silent revoked reader outlived the TTL: %+v", reply.Peers)
	}
}

func TestDocumentPresenceLeaveAndTTL(t *testing.T) {
	rig := presenceFixture(t)
	rig.beat("editor", "doc-a", caret("editor-tab-1", "e"))
	rig.beat("editor", "doc-a", caret("editor-tab-2", "e2"))
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "o")); len(reply.Peers) != 1 || reply.Peers[0].Tabs != 2 {
		t.Fatalf("one person's two tabs are one peer with a tab count: %+v", reply.Peers)
	}
	if code, _ := rig.beat("editor", "doc-a", `{"client":"editor-tab-1","leave":true}`); code != 200 {
		t.Fatalf("leave %d", code)
	}
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "o")); len(reply.Peers) != 1 || reply.Peers[0].Client != "editor-tab-2" || reply.Peers[0].Tabs != 1 {
		t.Fatalf("after leave: %+v", reply.Peers)
	}
	base := time.Now()
	rig.router.documentPresenceHub.now = func() time.Time { return base.Add(documentPresenceTTL - time.Second) }
	rig.beat("owner", "doc-a", caret("owner-tab-1", "o"))
	rig.router.documentPresenceHub.now = func() time.Time { return base.Add(documentPresenceTTL + time.Second) }
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "o")); len(reply.Peers) != 0 {
		t.Fatalf("expired tab still present: %+v", reply.Peers)
	}
}

func TestDocumentPresenceColoursAvoidCollisionsPerDocument(t *testing.T) {
	hub := newDocumentPresenceHub()
	room := documentPresenceRoom("ws", "page")
	seen := map[int]string{}
	for i := 0; i < documentPresenceColors; i++ {
		user := "user-" + string(rune('a'+i))
		color, _, ok := hub.beat(room, user, "tab-"+user+"-0001", user, "", "")
		if !ok {
			t.Fatal("room full too early")
		}
		if other, taken := seen[color]; taken {
			t.Fatalf("%s got colour %d already held by %s", user, color, other)
		}
		seen[color] = user
		// Deterministic: the same person on the same document keeps their colour.
		if again, _, _ := hub.beat(room, user, "tab-"+user+"-0001", user, "", ""); again != color {
			t.Fatalf("%s colour changed %d -> %d", user, color, again)
		}
	}
}

func TestDocumentPresenceIsReadScopedAndHumanOnly(t *testing.T) {
	if scopeForRoute("/api/v1/pages/{slug}/document/presence") != "pages:read" {
		t.Fatal("presence is read authority; a pages:read CLI token must be able to heartbeat")
	}
	rig := presenceFixture(t)
	req := httptest.NewRequest("POST", "/api/v1/pages/doc-a/document/presence", strings.NewReader(caret("agent-tab-1", "a")))
	req.Header.Set("Authorization", "Bearer "+rig.tokens["owner"])
	req.Header.Set("X-Workspace-ID", rig.ws)
	req.Header.Set("X-Internal-Token", "synthetic-agent")
	w := httptest.NewRecorder()
	rig.router.mux.ServeHTTP(w, req)
	if w.Code == 200 {
		t.Fatal("an agent credential joined human presence")
	}
}

func TestDocumentPresenceNeverShowsAnEmailAsTheName(t *testing.T) {
	rig := presenceFixture(t)
	mustExec(t, rig.router.db, `UPDATE users SET full_name='' WHERE id='presence-reader'`)
	rig.beat("reader", "doc-a", caret("reader-tab-1", "r"))
	_, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "o"))
	if got := presenceNames(reply.Peers); len(got) != 1 || got[0] != "Workspace member" {
		t.Fatalf("a member without a display name is shown as %v", got)
	}
}

// Tabs that vanish without a leave expire server-wide: stale entries in rooms
// nobody opens again never fill the server-wide limit.
func TestDocumentPresenceStaleRoomsNeverExhaustTheServer(t *testing.T) {
	hub := newDocumentPresenceHub()
	base := time.Now()
	hub.now = func() time.Time { return base }
	for i := 0; hub.total < documentPresenceTotalLimit; i++ {
		room := documentPresenceRoom("ws", "page-"+strconv.Itoa(i))
		if _, _, ok := hub.beat(room, "user-"+strconv.Itoa(i), "tab-00000001", "Name", "", ""); !ok {
			t.Fatalf("filling the hub refused at %d", hub.total)
		}
	}
	if _, _, ok := hub.beat(documentPresenceRoom("ws", "fresh"), "late-user", "tab-00000001", "Late", "", ""); ok {
		t.Fatal("the server-wide limit must hold while entries are live")
	}
	hub.now = func() time.Time { return base.Add(documentPresenceTTL + time.Second) }
	if _, _, ok := hub.beat(documentPresenceRoom("ws", "fresh"), "late-user", "tab-00000001", "Late", "", ""); !ok {
		t.Fatal("expired entries in untouched rooms still block new people")
	}
	if hub.total != 1 || len(hub.rooms) != 1 {
		t.Fatalf("expired entries kept: total %d rooms %d", hub.total, len(hub.rooms))
	}
}

// One person cannot fill a document's room with made-up tab ids.
func TestDocumentPresenceCapsTabsPerPerson(t *testing.T) {
	rig := presenceFixture(t)
	for i := 0; i < documentPresenceUserTabLimit; i++ {
		if code, _ := rig.beat("reader", "doc-a", caret("reader-tab-"+strconv.Itoa(i), "r")); code != 200 {
			t.Fatalf("tab %d refused with %d", i, code)
		}
	}
	if code, _ := rig.beat("reader", "doc-a", caret("reader-tab-extra", "r")); code != 429 {
		t.Fatalf("a ninth tab of one person accepted with %d", code)
	}
	if code, reply := rig.beat("editor", "doc-a", caret("editor-tab-1", "e")); code != 200 || len(reply.Peers) != 1 || reply.Peers[0].Tabs != documentPresenceUserTabLimit {
		t.Fatalf("another person locked out: %d %+v", code, reply.Peers)
	}
}

// One account cannot hold the server-wide limit across many documents.
func TestDocumentPresenceCapsTabsPerPersonServerWide(t *testing.T) {
	hub := newDocumentPresenceHub()
	for i := 0; i < documentPresenceUserTotalLimit; i++ {
		if _, _, ok := hub.beat(documentPresenceRoom("ws", "page-"+strconv.Itoa(i)), "busy-user", "tab-00000001", "Busy", "", ""); !ok {
			t.Fatalf("tab %d refused", i)
		}
	}
	if _, _, ok := hub.beat(documentPresenceRoom("ws", "one-more"), "busy-user", "tab-00000001", "Busy", "", ""); ok {
		t.Fatal("one person exceeded the server-wide tab limit")
	}
	if _, _, ok := hub.beat(documentPresenceRoom("ws", "one-more"), "other-user", "tab-00000001", "Other", "", ""); !ok {
		t.Fatal("another person locked out")
	}
	hub.leave(documentPresenceRoom("ws", "page-0"), "busy-user", "tab-00000001")
	if _, _, ok := hub.beat(documentPresenceRoom("ws", "one-more"), "busy-user", "tab-00000001", "Busy", "", ""); !ok {
		t.Fatal("a leave must free the person's slot")
	}
	if hub.users["busy-user"] != documentPresenceUserTotalLimit || hub.total != documentPresenceUserTotalLimit+1 {
		t.Fatalf("counts drifted: user %d total %d", hub.users["busy-user"], hub.total)
	}
}

// #5166 debt: a revoked credential, an ended membership or a role change
// removes the person's presence at once, not after the TTL.
func TestDocumentPresenceEndsAtOnceOnRevokeAndMembershipChange(t *testing.T) {
	present := func(rig *presenceRig, name string) bool {
		_, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "o"))
		for _, peer := range reply.Peers {
			if peer.Name == name {
				return true
			}
		}
		return false
	}
	call := func(rig *presenceRig, who, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+rig.tokens[who])
		req.Header.Set("X-Workspace-ID", rig.ws)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		rig.router.mux.ServeHTTP(w, req)
		return w.Code
	}
	t.Run("CLI token revoked", func(t *testing.T) {
		rig := presenceFixture(t)
		rig.beat("editor", "doc-a", caret("editor-tab-1", "e"))
		if !present(rig, "Erin Editor") {
			t.Fatal("setup: editor not present")
		}
		if code := call(rig, "editor", "DELETE", "/api/v1/auth/cli-tokens/clt-presence-editor", ""); code != 200 {
			t.Fatalf("revoke token: %d", code)
		}
		if present(rig, "Erin Editor") {
			t.Fatal("a revoked token keeps its tab present until the TTL")
		}
	})
	t.Run("revoked while the heartbeat is in flight", func(t *testing.T) {
		rig := presenceFixture(t)
		rig.beat("editor", "doc-a", caret("editor-tab-1", "e"))
		documentPresenceBeforeBeat = func() {
			documentPresenceBeforeBeat = nil
			rig.router.authMw.streams.NotifyTokenRevoked("clt-presence-editor")
		}
		t.Cleanup(func() { documentPresenceBeforeBeat = nil })
		if code, _ := rig.beat("editor", "doc-a", caret("editor-tab-1", "e")); code == 200 {
			t.Fatal("a heartbeat revoked mid-flight was accepted")
		}
		if present(rig, "Erin Editor") {
			t.Fatal("a heartbeat that raced the revocation put the tab back until the TTL")
		}
	})
	t.Run("membership removed", func(t *testing.T) {
		rig := presenceFixture(t)
		rig.beat("editor", "doc-a", caret("editor-tab-1", "e"))
		var member string
		if err := rig.router.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id=? AND user_id='presence-editor'`, rig.ws).Scan(&member); err != nil {
			t.Fatal(err)
		}
		if code := call(rig, "owner", "DELETE", "/api/v1/workspaces/"+rig.ws+"/members/"+member, ""); code != 200 {
			t.Fatalf("remove member: %d", code)
		}
		if present(rig, "Erin Editor") {
			t.Fatal("a removed member stays present until the TTL")
		}
	})
	t.Run("role changed", func(t *testing.T) {
		rig := presenceFixture(t)
		rig.beat("editor", "doc-a", caret("editor-tab-1", "e"))
		var member string
		if err := rig.router.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id=? AND user_id='presence-editor'`, rig.ws).Scan(&member); err != nil {
			t.Fatal(err)
		}
		if code := call(rig, "owner", "PATCH", "/api/v1/workspaces/"+rig.ws+"/members/"+member, `{"role":"VIEWER"}`); code != 200 {
			t.Fatalf("change role: %d", code)
		}
		if present(rig, "Erin Editor") {
			t.Fatal("a role change keeps the old presence until the TTL")
		}
		// Still able to read: the next heartbeat brings the person back.
		rig.beat("editor", "doc-a", caret("editor-tab-1", "e"))
		if !present(rig, "Erin Editor") {
			t.Fatal("a member who can still read is not re-admitted")
		}
	})
}

func TestDocumentPresenceCredentialRevocationKinds(t *testing.T) {
	hub := newDocumentPresenceHub()
	room, other := documentPresenceRoom("ws", "a"), documentPresenceRoom("ws2", "b")
	seed := func() {
		hub.beatAs(room, "u1", "tab-session1", "s1", "", "U1", "", "")
		hub.beatAs(room, "u1", "tab-token001", "", "t1", "U1", "", "")
		hub.beatAs(other, "u1", "tab-session2", "s2", "", "U1", "", "")
		hub.beatAs(room, "u2", "tab-session3", "s3", "", "U2", "", "")
	}
	tabs := func() []string {
		out := []string{}
		for _, entries := range hub.rooms {
			for _, e := range entries {
				out = append(out, e.client)
			}
		}
		sort.Strings(out)
		return out
	}
	for _, tc := range []struct{ kind, id, want string }{
		{"session", "s1", "tab-session2 tab-session3 tab-token001"},
		{"token", "t1", "tab-session1 tab-session2 tab-session3"},
		{"user-session", "u1", "tab-session3 tab-token001"},
		{"user-token", "u1", "tab-session1 tab-session2 tab-session3"},
		{"account", "u1", "tab-session3"},
	} {
		hub.forget("", func(*documentPresenceEntry) bool { return true })
		seed()
		hub.credentialRevoked(tc.kind, tc.id)
		if got := strings.Join(tabs(), " "); got != tc.want {
			t.Errorf("%s %s: left %q, want %q", tc.kind, tc.id, got, tc.want)
		}
		if hub.total != len(tabs()) {
			t.Errorf("%s: total %d for %d tabs", tc.kind, hub.total, len(tabs()))
		}
	}
	hub.forget("", func(*documentPresenceEntry) bool { return true })
	seed()
	hub.workspaceGone("ws")
	if got := strings.Join(tabs(), " "); got != "tab-session2" {
		t.Errorf("workspace gone: left %q", got)
	}
	hub.forget("", func(*documentPresenceEntry) bool { return true })
	seed()
	hub.memberAccessChanged("ws", "u1")
	if got := strings.Join(tabs(), " "); got != "tab-session2 tab-session3" {
		t.Errorf("member access changed: left %q", got)
	}
	var none *documentPresenceHub
	none.memberAccessChanged("ws", "u1")
	none.workspaceGone("ws")
}

// Presence is per person (CO01: the right name and colour; two windows of one
// Owner are not two participants): a peer with several tabs is one entry with
// a tab count and the caret of the tab that moved last, never one caret per
// tab. A person's own other tabs are never listed as peers.
func TestDocumentPresenceIsOneEntryPerPerson(t *testing.T) {
	rig := presenceFixture(t)
	base := time.Now()
	at := func(d time.Duration) { rig.router.documentPresenceHub.now = func() time.Time { return base.Add(d) } }
	at(0)
	rig.beat("owner", "doc-a", caret("owner-tab-1", "first"))
	at(100 * time.Millisecond)
	rig.beat("owner", "doc-a", caret("owner-tab-2", "second"))
	at(200 * time.Millisecond)
	rig.beat("owner", "doc-a", caret("owner-tab-3", "third"))
	// Tab 1 moves last; tabs 2 and 3 keep heartbeating without moving.
	at(300 * time.Millisecond)
	rig.beat("owner", "doc-a", caret("owner-tab-1", "moved"))
	at(400 * time.Millisecond)
	rig.beat("owner", "doc-a", caret("owner-tab-2", "second"))
	rig.beat("owner", "doc-a", caret("owner-tab-3", "third"))
	code, reply := rig.beat("editor", "doc-a", caret("editor-tab-1", "e"))
	if code != 200 || len(reply.Peers) != 1 {
		t.Fatalf("three Owner tabs must be one peer: %d %+v", code, reply.Peers)
	}
	peer := reply.Peers[0]
	moved := base64.StdEncoding.EncodeToString([]byte("moved"))
	if peer.Tabs != 3 || peer.Client != "owner-tab-1" || peer.Anchor != moved || peer.Head != moved {
		t.Fatalf("want the last-moved tab's caret and tabs=3, got %+v", peer)
	}
	// A later heartbeat without a caret (the CLI, a node selection) never hides that caret.
	at(500 * time.Millisecond)
	rig.beat("owner", "doc-a", `{"client":"owner-cli-0001"}`)
	if _, reply := rig.beat("editor", "doc-a", caret("editor-tab-1", "e")); len(reply.Peers) != 1 || reply.Peers[0].Client != "owner-tab-1" || reply.Peers[0].Tabs != 4 {
		t.Fatalf("a caretless tab hid the moved caret: %+v", reply.Peers)
	}
	if _, own := rig.beat("owner", "doc-a", caret("owner-tab-2", "second")); len(own.Peers) != 1 || own.Peers[0].UserID == peer.UserID {
		t.Fatalf("own tabs listed as peers: %+v", own.Peers)
	}
}
