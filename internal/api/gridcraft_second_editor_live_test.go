package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/ws"
)

// A named second editor of a published workbook takes part in live editing
// like the owner: it opens its own editor session, edits through it, is
// subscribed to the Page channel and receives workbook.committed, and reads
// the commits feed. The feed starts at the revision the owner published, so
// the private history before publication stays with the owner. Revoking the
// editor ends all of it at the next request.
func TestGridCraftSecondEditorLiveSessionsPushAndFeedFloor(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	r, err := NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Shutdown)
	f.r = r
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	bob := mintTokenFor(t, f.db, "bob", "workbookbob000000000000000")
	q := "?workspace_id=ws-old"
	base := liveWorkbook(t, f, ann, "ann", "live-shared")

	// Private history: the owner edits before publication.
	owner := openEditorSession(t, f, ann, base)
	wantCode(t, f.do(ann, "POST", base+"/edits"+q, cellEdit(owner.SessionID, 1, 1, "B2", "private-draft")), 200, "owner private edit")
	wantCode(t, f.do(bob, "POST", base+"/sessions"+q, ""), 404, "no session before sharing")

	wantCode(t, f.do(ann, "POST", base+"/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "share with bob")

	// The editor may subscribe to the Page channel the push uses.
	auth := ws.NewDBChannelAuthorizer(f.db)
	if ok, err := auth.CanSubscribe(t.Context(), "bob", "page:live-shared"); err != nil || !ok {
		t.Fatalf("editor cannot subscribe to the workbook channel: %v %v", ok, err)
	}
	observer := hub.AddObserver("page:live-shared", "bob", 16)
	t.Cleanup(func() { hub.RemoveObserver("page:live-shared", observer) })

	editor := openEditorSession(t, f, bob, base)
	if editor.Rev != 2 {
		t.Fatalf("editor session rev %d", editor.Rev)
	}
	wantCode(t, f.do(bob, "POST", base+"/edits"+q, cellEdit(editor.SessionID, 1, 2, "C3", "bob-value")), 200, "editor edit")
	deadline := time.After(5 * time.Second)
	for pushed := false; !pushed; {
		select {
		case raw := <-observer.Frames():
			var frame struct {
				Type    string         `json:"type"`
				Payload map[string]any `json:"payload"`
			}
			if json.Unmarshal(raw, &frame) == nil && frame.Type == "workbook.committed" && frame.Payload["rev"] == float64(3) {
				pushed = true
			}
		case <-deadline:
			t.Fatal("editor's revision was not pushed on the Page channel")
		}
	}
	// The owner's session does not serve the editor.
	wantError(t, f.do(bob, "POST", base+"/edits"+q, cellEdit(owner.SessionID, 9, 3, "D4", "x")), 404, "Workbook not found", "editor using the owner's session")

	// Feed: the editor starts at the published revision; the owner sees all.
	gone := f.do(bob, "GET", base+"/commits"+q+"&since=0", "")
	wantCode(t, gone, 410, "editor below the publication floor")
	if !strings.Contains(gone.Body.String(), `"min_since":2`) || strings.Contains(gone.Body.String(), "private-draft") {
		t.Fatalf("editor floor %s", gone.Body.String())
	}
	feed := f.do(bob, "GET", base+"/commits"+q+"&since=2", "")
	wantCode(t, feed, 200, "editor feed after publication")
	if !strings.Contains(feed.Body.String(), "bob-value") || strings.Contains(feed.Body.String(), "private-draft") {
		t.Fatalf("editor feed %s", feed.Body.String())
	}
	full := f.do(ann, "GET", base+"/commits"+q+"&since=0", "")
	wantCode(t, full, 200, "owner full feed")
	if !strings.Contains(full.Body.String(), "private-draft") || !strings.Contains(full.Body.String(), "bob-value") {
		t.Fatalf("owner feed %s", full.Body.String())
	}

	// Revocation ends the editor's session, edits and feed.
	wantCode(t, f.do(ann, "DELETE", "/api/v1/pages/live-shared/grants"+q+"&subject_type=user&subject=bob@ex.com", ""), 200, "revoke editor")
	wantCode(t, f.do(bob, "POST", base+"/edits"+q, cellEdit(editor.SessionID, 2, 3, "E5", "late")), 404, "revoked edit")
	wantCode(t, f.do(bob, "POST", base+"/sessions"+q, `{"session_id":"`+editor.SessionID+`"}`), 404, "revoked heartbeat")
	wantCode(t, f.do(bob, "GET", base+"/commits"+q+"&since=2", ""), 404, "revoked feed")
}
