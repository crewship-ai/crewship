package api

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/design"
	"github.com/gorilla/websocket"
)

func TestDesignSharedOnlyReaders(t *testing.T) {
	for _, level := range []string{"read", "comment"} {
		t.Run(level, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			user := "shared-only-reader"
			f.addUser(t, user)
			if w := f.grant(t, user, level); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			if w := f.call(t, user, "get", ""); w.Code != 404 {
				t.Errorf("unshared content exposed: %d %s", w.Code, w.Body)
			}
			for _, step := range []struct{ op, body string }{
				{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"SHARED"}}]}`},
				{"share", `{"version":2}`},
				{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"PRIVATE_SAVED"}}]}`},
			} {
				if w := f.call(t, f.owner, step.op, step.body); w.Code != 200 {
					t.Fatal(step.op, w.Code, w.Body)
				}
			}
			f.liveOnly(t, `[{"op":"upsert","node":{"id":"private-live","type":"text","x":0,"y":40,"width":200,"height":30,"text":"PRIVATE_LIVE"}}]`)
			w := f.call(t, user, "get", "")
			var view struct {
				Version       int64             `json:"version"`
				LatestVersion int64             `json:"latest_version"`
				SharedVersion int64             `json:"shared_version"`
				Nodes         []json.RawMessage `json:"nodes"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || view.Version != 2 || view.LatestVersion != 2 || view.SharedVersion != 2 || len(view.Nodes) != 1 || !bytes.Contains(view.Nodes[0], []byte("SHARED")) || bytes.Contains(w.Body.Bytes(), []byte("PRIVATE_")) {
				t.Errorf("Read/Comment escaped the shared snapshot: %d %s", w.Code, w.Body)
			}
			if level == "comment" {
				w := f.call(t, user, "comment", `{"version":2,"path":"design.json","selector":"title","quote":"SHARED","text":"Shared feedback"}`)
				if w.Code != 201 {
					t.Errorf("cannot comment on displayed shared version: %d %s", w.Code, w.Body)
				}
				w = f.call(t, user, "comment", `{"version":3,"path":"design.json","selector":"title","quote":"PRIVATE_SAVED","text":"Private feedback"}`)
				if w.Code != 409 && w.Code != 404 {
					t.Errorf("comment accepted on unshared content: %d %s", w.Code, w.Body)
				}
			}
			socket := f.dial(t, user)
			empty, err := design.Run(t.Context(), design.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if err = socket.WriteMessage(websocket.BinaryMessage, empty.Hello); err != nil {
				t.Fatal(err)
			}
			_ = socket.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, frame, err := socket.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			initial, err := design.Run(t.Context(), design.Request{Frame: frame})
			if err != nil || len(initial.Nodes) != 1 || !bytes.Contains(initial.Nodes[0], []byte("SHARED")) {
				t.Fatalf("readonly sync exposed live state: %s %v", initial.Nodes, err)
			}
			if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"PRIVATE_BROADCAST"}}]}`); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			// A queued presence marker follows all updates from the completed
			// mutation. This checks delivery ordering without a sleep-based test.
			marker := []byte{1, 0}
			room := f.h.designRoom(artifacts.VersionKey{WorkspaceID: f.workspace, PageID: f.page})
			room.mu.Lock()
			for client := range room.clients {
				if client.actor == user {
					_ = client.enqueue(marker)
				}
			}
			room.mu.Unlock()
			_ = socket.SetReadDeadline(time.Now().Add(5 * time.Second))
			for {
				_, frame, err = socket.ReadMessage()
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(frame, marker) {
					break
				}
				kind, _, err := designSyncKind(frame)
				if err != nil || kind == 0 {
					t.Fatalf("live document update reached shared-only reader: %x %v", frame, err)
				}
			}
		})
	}
}

func TestDesignWorkspaceAdminNeedsExplicitPageAuthority(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	user := "design-workspace-admin"
	f.addUser(t, user)
	mustExec(t, f.h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
	for _, step := range []struct{ op, body string }{{"get", ""}, {"apply", `{"operations":[]}`}} {
		if w := f.call(t, user, step.op, step.body); w.Code != 404 {
			t.Errorf("workspace role bypassed Design authority: %s %d %s", step.op, w.Code, w.Body)
		}
	}
	if w := f.grant(t, user, "read"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.call(t, f.owner, "share", `{"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.call(t, user, "get", ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"can_edit":false`)) {
		t.Fatalf("explicit Read grant changed into admin Edit: %d %s", w.Code, w.Body)
	}
	if w := f.grant(t, user, "write"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.call(t, user, "get", ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"can_edit":true`)) {
		t.Fatalf("explicit Edit grant unavailable: %d %s", w.Code, w.Body)
	}
}
