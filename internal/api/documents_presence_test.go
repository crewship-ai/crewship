package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
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

func names(peers []documentPresencePeer) []string {
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
	if got := names(reply.Peers); len(got) != 1 || got[0] != "Erin Editor" {
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
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "o")); len(reply.Peers) != 2 || reply.Peers[0].Color != reply.Peers[1].Color {
		t.Fatalf("one person's two tabs: %+v", reply.Peers)
	}
	if code, _ := rig.beat("editor", "doc-a", `{"client":"editor-tab-1","leave":true}`); code != 200 {
		t.Fatalf("leave %d", code)
	}
	if _, reply := rig.beat("owner", "doc-a", caret("owner-tab-1", "o")); len(reply.Peers) != 1 || reply.Peers[0].Client != "editor-tab-2" {
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
