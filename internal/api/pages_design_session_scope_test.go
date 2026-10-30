package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/design"
	"github.com/gorilla/websocket"
)

func (f *designAuthorityFixture) workspaceEpoch(t *testing.T) int64 {
	t.Helper()
	var epoch int64
	if err := f.h.db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id=?`, f.workspace).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// designSocketLive proves the admitted socket still passes the per-frame
// authority fence: a sync step 1 goes through designTx under the pinned
// admission and must be answered with sync step 2.
func designSocketLive(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err != nil {
		t.Fatalf("socket closed: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("admitted socket was retired by an unrelated change: %v", err)
		}
		kind, sub, err := designSyncKind(raw)
		if err != nil {
			t.Fatal(err)
		}
		if kind == 0 && sub == 1 {
			return
		}
	}
}

// Unrelated workspace changes move workspaces.authority_revision but must not
// retire a live Design socket (and its unconfirmed edits). A change that
// concerns this person or this Page still closes it.
func TestDesignSocketSurvivesUnrelatedWorkspaceAuthorityChanges(t *testing.T) {
	const user = "design-scope-editor"
	cases := []struct {
		name   string
		change func(t *testing.T, f *designAuthorityFixture)
		keep   bool
	}{
		{"another page created", func(t *testing.T, f *designAuthorityFixture) {
			if w := f.call(t, f.owner, "create", `{"slug":"other-board","name":"Unrelated"}`); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
		}, true},
		{"grant on another page to another member", func(t *testing.T, f *designAuthorityFixture) {
			f.addUser(t, "design-scope-bystander")
			if w := f.call(t, f.owner, "create", `{"slug":"other-board","name":"Unrelated"}`); w.Code != 201 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			mustExec(t, f.h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) SELECT id,'user','design-scope-bystander','write',? FROM pages WHERE workspace_id=? AND slug='other-board'`, f.owner, f.workspace)
		}, true},
		{"new workspace member", func(t *testing.T, f *designAuthorityFixture) {
			f.addUser(t, "design-scope-newcomer")
		}, true},
		{"own grant revoked", func(t *testing.T, f *designAuthorityFixture) {
			mustExec(t, f.h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id=?`, f.page, user)
		}, false},
		{"own grant revoked and restored", func(t *testing.T, f *designAuthorityFixture) {
			f.regrantSameTransaction(t, user, "write")
		}, false},
		{"removed from workspace", func(t *testing.T, f *designAuthorityFixture) {
			mustExec(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, user)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			f.addUser(t, user)
			if w := f.grant(t, user, "write"); w.Code != 200 {
				t.Fatalf("grant %d %s", w.Code, w.Body)
			}
			socket := f.dial(t, user)
			epoch, revision := f.workspaceEpoch(t), f.revision(t)
			tc.change(t, f)
			if f.workspaceEpoch(t) == epoch {
				t.Fatal("setup did not move the workspace delivery epoch; the case proves nothing")
			}
			if !tc.keep {
				if err := socket.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err == nil {
					designAuthorityClosed(t, socket)
				}
				return
			}
			designSocketLive(t, socket)
			update, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"kept-edit","type":"rect","x":0,"y":0,"width":10,"height":10}}]`)})
			if err != nil {
				t.Fatal(err)
			}
			if err = socket.WriteMessage(websocket.BinaryMessage, designUpdateFrame(update.Update)); err != nil {
				t.Fatal(err)
			}
			designSocketLive(t, socket)
			if f.revision(t) <= revision {
				t.Fatal("edit on the kept socket was not applied")
			}
		})
	}
}
