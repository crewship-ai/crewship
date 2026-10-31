package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"context"
	"database/sql"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/ws"
)

type recheckWatch struct {
	user string
	obs  *ws.Observer
}

func recheckFrames(t *testing.T, hub *ws.Hub, w recheckWatch) []ws.ServerMessage {
	t.Helper()
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
	_ = httptest.NewRecorder
}
