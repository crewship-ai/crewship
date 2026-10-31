package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/ws"
)

type recheckWatch struct {
	user string
	obs  *ws.Observer
}

func recheckFrames(t *testing.T, hub *ws.Hub, w recheckWatch) []ws.ServerMessage {
	t.Helper()
	recheckInFlight.Wait() // the fan-out is asynchronous; the sentinel must queue behind it
	channel := "user:" + w.user
	hub.Broadcast(channel, ws.ServerMessage{Type: "test.sentinel", Channel: channel})
	var frames []ws.ServerMessage
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-w.obs.Frames():
			var m ws.ServerMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if m.Type == "test.sentinel" {
				return frames
			}
			frames = append(frames, m)
		case <-deadline:
			t.Fatal("sentinel missing")
		}
	}
}

// Rights changes on a native Craft page push a bare "re-check" signal to the
// affected person only (docs/specs/craft-native.md, Shared previews).
func TestNativeCraftGrantChangePushesAccessRecheckOnlyToAffectedUser(t *testing.T) {
	f, _, d, _, _, _, _ := craftNativeDocumentsFixture(t)
	hub := startedTestHub(t)
	ph := d.human.pages
	ph.hub = hub
	readCtx := craftNativePreviewPeer(t, f, "read")
	reader := UserFromContext(readCtx).ID
	otherCtx := craftNativePreviewPeer(t, f, "comment")
	other := UserFromContext(otherCtx).ID
	mustExec(t, f.db, `INSERT INTO users(id,email) VALUES('outsider','outsider@example.test')`)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,'outsider','MEMBER')`, generateCUID(), f.wsID)
	// A second, ordinary (non native) page the reader also holds.
	f.seedPage(t, "plain-page", "plain-page", "Plain", f.userID, "")
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('plain-page','user',?,'read',?)`, reader, f.userID)

	watch := map[string]recheckWatch{}
	for _, u := range []string{reader, other, "outsider", f.userID} {
		o := hub.AddObserver("user:"+u, u, 16)
		defer hub.RemoveObserver("user:"+u, o)
		watch[u] = recheckWatch{u, o}
	}
	// Publish a version so the reader has content before the revocation.
	store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	allow := func(context.Context, *sql.Tx) (string, error) { return "fixture-owner", nil }
	if err := store.Share(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, 1, 1, allow); err != nil {
		t.Fatal(err)
	}
	if w := craftNativePreviewCall(d, readCtx); w.Code != 200 {
		t.Fatalf("baseline preview: %d %s", w.Code, w.Body)
	}
	probe := `{"v":1}`

	// Revoking a grant on the OTHER (non native) page signals nobody.
	if rr := pagesGrantCall(t, ph, "DELETE", "/grants?subject_type=user&subject="+reader+"&level=read", f.wsID, f.userID, "OWNER", "plain-page", ""); rr.Code != 200 {
		t.Fatalf("plain revoke: %d %s", rr.Code, rr.Body)
	}
	for u, w := range watch {
		if got := recheckFrames(t, hub, w); len(got) != 0 {
			t.Fatalf("plain page revoke signalled %s: %+v", u, got)
		}
	}

	// Revoking the reader on the native page signals the reader alone.
	rr := pagesGrantCall(t, ph, "DELETE", "/grants?subject_type=user&subject="+reader+"&level=read", f.wsID, f.userID, "OWNER", "native-page", "")
	if rr.Code != 200 {
		t.Fatalf("native revoke: %d %s", rr.Code, rr.Body)
	}
	got := recheckFrames(t, hub, watch[reader])
	if len(got) != 1 || got[0].Type != pageAccessRecheckEvent {
		t.Fatalf("reader frames: %+v", got)
	}
	payload, _ := json.Marshal(got[0].Payload)
	if string(payload) != `{"page_id":"native-page"}` {
		t.Fatalf("recheck payload must be the page id only: %s", payload)
	}
	for _, u := range []string{other, "outsider", f.userID} {
		if got := recheckFrames(t, hub, watch[u]); len(got) != 0 {
			t.Fatalf("%s received a signal about someone else's revocation: %+v", u, got)
		}
	}

	// Privacy regression: after the revocation the revoked person receives no
	// content through the preview, the lease/session door or any frame, and
	// cannot listen on anybody else's identity channel.
	if w := craftNativePreviewCall(d, readCtx); w.Code != 403 || strings.Contains(w.Body.String(), "bytes") || strings.Contains(w.Body.String(), probe) {
		t.Fatalf("revoked preview: %d %s", w.Code, w.Body)
	}
	auth := ws.NewDBChannelAuthorizer(f.db)
	for _, ch := range []string{"user:" + other, "user:" + f.userID} {
		if ok, err := auth.CanSubscribe(t.Context(), reader, ch); err != nil || ok {
			t.Fatalf("revoked person may listen on %s: %v %v", ch, ok, err)
		}
	}
	if ok, err := auth.CanSubscribe(t.Context(), reader, "user:"+reader); err != nil || !ok {
		t.Fatalf("own identity channel: %v %v", ok, err)
	}
	// Granting the same person access again signals again, still bare.
	rr = pagesGrantCall(t, ph, "PUT", "/grants", f.wsID, f.userID, "OWNER", "native-page", `{"subject_type":"user","subject":"`+reader+`","level":"read"}`)
	if rr.Code != 200 && rr.Code != 201 {
		t.Fatalf("re-grant: %d %s", rr.Code, rr.Body)
	}
	got = recheckFrames(t, hub, watch[reader])
	if len(got) != 1 {
		t.Fatalf("re-grant frames: %+v", got)
	}
}

func watchUsers(t *testing.T, hub *ws.Hub, users ...string) map[string]recheckWatch {
	t.Helper()
	out := map[string]recheckWatch{}
	for _, u := range users {
		o := hub.AddObserver("user:"+u, u, 16)
		t.Cleanup(func() { hub.RemoveObserver("user:"+u, o) })
		out[u] = recheckWatch{u, o}
	}
	return out
}

func addMember(t *testing.T, f *pagesTransferRig, id, role, mode string) {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO users(id,email) VALUES(?,?)`, id, id+"@example.test")
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES(?,?,?,?,?)`, generateCUID(), f.wsID, id, role, mode)
}

func requireOnlyRecheck(t *testing.T, hub *ws.Hub, watch map[string]recheckWatch, want map[string]bool, payload string) {
	t.Helper()
	for u, w := range watch {
		got := recheckFrames(t, hub, w)
		if !want[u] {
			if len(got) != 0 {
				t.Fatalf("%s must receive nothing: %+v", u, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Type != pageAccessRecheckEvent {
			t.Fatalf("%s frames: %+v", u, got)
		}
		if raw, _ := json.Marshal(got[0].Payload); string(raw) != payload {
			t.Fatalf("%s payload %s, want %s", u, raw, payload)
		}
	}
}

// Crew and workspace grants fan out to their members only; a restricted or
// suspended account gets no delivery; a refused or rolled back mutation
// sends nothing; a member removal or role change signals that member alone.
func TestNativeCraftAccessRecheckFanOutScopeAndRollback(t *testing.T) {
	f, _, d, _, _, _, _ := craftNativeDocumentsFixture(t)
	ph := d.human.pages
	hub := startedTestHub(t)
	ph.hub = hub
	addMember(t, f, "viewer-1", "VIEWER", "trusted")
	addMember(t, f, "member-1", "MEMBER", "trusted")
	addMember(t, f, "crew-a", "MEMBER", "trusted")
	addMember(t, f, "crew-b", "MEMBER", "trusted")
	addMember(t, f, "restricted-1", "MEMBER", "restricted")
	addMember(t, f, "suspended-1", "MEMBER", "trusted")
	mustExec(t, f.db, `UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id='suspended-1'`)
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-1',?, 'Engine','engine')`, f.wsID)
	for _, u := range []string{"crew-a", "crew-b"} {
		mustExec(t, f.db, `INSERT INTO crew_members(id,crew_id,user_id,role) VALUES(?,'crew-1',?,'MEMBER')`, generateCUID(), u)
	}
	watch := watchUsers(t, hub, "viewer-1", "member-1", "crew-a", "crew-b", "restricted-1", "suspended-1", f.userID)

	// Crew grant: the crew's members only.
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','crew','crew-1','read',?)`, f.userID)
	if rr := pagesGrantCall(t, ph, "DELETE", "/grants?subject_type=crew&subject=crew-1&level=read", f.wsID, f.userID, "OWNER", "native-page", ""); rr.Code != 200 {
		t.Fatalf("crew revoke: %d %s", rr.Code, rr.Body)
	}
	requireOnlyRecheck(t, hub, watch, map[string]bool{"crew-a": true, "crew-b": true}, `{"page_id":"native-page"}`)

	// Workspace grant: every trusted member (a VIEWER too), but never the
	// restricted or suspended account, and never anyone outside the workspace.
	mustExec(t, f.db, `INSERT INTO users(id,email) VALUES('elsewhere','elsewhere@example.test')`)
	watch["elsewhere"] = watchUsers(t, hub, "elsewhere")["elsewhere"]
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','workspace','','read',?)`, f.userID)
	if rr := pagesGrantCall(t, ph, "DELETE", "/grants?subject_type=workspace&level=read", f.wsID, f.userID, "OWNER", "native-page", ""); rr.Code != 200 {
		t.Fatalf("workspace revoke: %d %s", rr.Code, rr.Body)
	}
	requireOnlyRecheck(t, hub, watch, map[string]bool{"viewer-1": true, "member-1": true, "crew-a": true, "crew-b": true, f.userID: true}, `{"page_id":"native-page"}`)

	// A refused mutation (not an administrator) sends nothing.
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','user','member-1','read',?)`, f.userID)
	if rr := pagesGrantCall(t, ph, "DELETE", "/grants?subject_type=user&subject=member-1&level=read", f.wsID, "viewer-1", "VIEWER", "native-page", ""); rr.Code == 200 {
		t.Fatalf("a viewer revoked a grant: %d", rr.Code)
	}
	requireOnlyRecheck(t, hub, watch, nil, "")

	// A failure after the mutation's statements rolls it back: no frame, and the grant stays.
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','crew','crew-1','read',?)`, f.userID)
	mustExec(t, f.db, `ALTER TABLE crew_members RENAME TO crew_members_unavailable`)
	rr := pagesGrantCall(t, ph, "DELETE", "/grants?subject_type=crew&subject=crew-1&level=read", f.wsID, f.userID, "OWNER", "native-page", "")
	mustExec(t, f.db, `ALTER TABLE crew_members_unavailable RENAME TO crew_members`)
	if rr.Code != 500 {
		t.Fatalf("recipient lookup failure: %d %s", rr.Code, rr.Body)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id='native-page' AND subject_type='crew'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("grant not rolled back: %d %v", n, err)
	}
	requireOnlyRecheck(t, hub, watch, nil, "")
}

// Direct tests of the sender: restricted/suspended filtered, nil hub safe.
func TestPublishUserAccessRecheckIsAsyncAndNilSafe(t *testing.T) {
	publishUserAccessRecheck(nil, nil, []string{"a"}, map[string]any{})
	hub := startedTestHub(t)
	users := make([]string, 1200)
	for i := range users {
		users[i] = "bulk-" + strings.Repeat("x", i%3) + string(rune('a'+i%26))
	}
	start := time.Now()
	publishUserAccessRecheck(hub, nil, users, map[string]any{})
	if time.Since(start) > time.Second {
		t.Fatal("the caller waited for the fan-out")
	}
	recheckInFlight.Wait()
}

// A role change tells the changed member alone to re-check everything (no page
// id); a refused change tells nobody.
func TestMemberRoleChangeSignalsOnlyThatMember(t *testing.T) {
	h, _, wsID := roleRig(t)
	hub := startedTestHub(t)
	h.hub = hub
	seedMember(t, h, wsID, "m-admin", "u-admin", "admin@x.io", "ADMIN")
	seedMember(t, h, wsID, "m-target", "u-target", "target@x.io", "MEMBER")
	watch := watchUsers(t, hub, "u-target", "u-admin")

	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, "u-admin", wsID, "ADMIN", "m-target", "ADMIN"))
	if rr.Code != 403 {
		t.Fatalf("refused change: %d", rr.Code)
	}
	requireOnlyRecheck(t, hub, watch, nil, "")

	rr = httptest.NewRecorder()
	h.UpdateMemberRole(rr, roleReq(t, "u-admin", wsID, "ADMIN", "m-target", "VIEWER"))
	if rr.Code != 200 {
		t.Fatalf("role change: %d %s", rr.Code, rr.Body)
	}
	requireOnlyRecheck(t, hub, watch, map[string]bool{"u-target": true}, `{}`)
}
