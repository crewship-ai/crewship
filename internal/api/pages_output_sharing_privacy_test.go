package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
	"golang.org/x/net/websocket"
)

// Exercise the workspace audiences through production authentication and
// routing. Board state is seeded in the shared durable schema so this test
// also runs in the Artifact delivery, before the Board API is enabled.
func TestOutputSharingAndFolderEventsKeepPrivateMetadataOutOfMemberRoutes(t *testing.T) {
	for _, kind := range []string{"artifact", "board", "empty-human", "legacy-panel"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _, workspace, owner := newPagesFixture(t)
			h.artifactStoragePath = storageDir(t)
			const member, reader = "ungranted-member", "explicit-reader"
			for _, user := range []string{member, reader} {
				pagesSeedUser(t, h, workspace, user, user+"@example.invalid", "MEMBER")
			}
			const slug, title = "private-output-slug-canary", "Private output title canary"
			body := fmt.Sprintf(`{"slug":%q,"name":%q,"panels":[],"allow_empty":true}`, slug, title)
			if kind == "legacy-panel" {
				body = pagesSpecBody(slug)
			}
			create := httptest.NewRecorder()
			h.Create(create, pagesGrantRequest(t, "POST", "/api/v1/pages", workspace, owner, "OWNER", body))
			artifactExpect(t, create, 201)
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			pageID := created.ID
			if kind == "artifact" {
				r := pagesGrantRequest(t, "POST", "/api/v1/pages/"+slug+"/artifact/revisions", workspace, owner, "OWNER", `{"html":"<h1>Private source canary</h1>","base_rev":0,"mutation_id":"initial-private-source"}`)
				r.SetPathValue("slug", slug)
				rr := httptest.NewRecorder()
				h.PublishArtifact(rr, r)
				artifactExpect(t, rr, 201)
			} else if kind == "board" {
				execOrFatal(t, h.db, `INSERT INTO page_boards(page_id,seq,format) VALUES(?,0,'crewship-board-excalidraw-0.18.1-v1')`, pageID)
			}
			folderSlugs := []string{"private-folder-one-canary", "private-folder-two-canary"}
			folderIDs := []string{"opaque-folder-one", "opaque-folder-two"}
			for i := range folderIDs {
				execOrFatal(t, h.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES(?,?,?,?,'crew-lookout',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, folderIDs[i], workspace, folderSlugs[i], "Private folder title canary")
			}
			const secret = "synthetic-output-sharing-session-secret-long-enough"
			validator, err := auth.NewJWTValidator(secret)
			if err != nil {
				t.Fatal(err)
			}
			tokens := map[string]string{}
			for _, user := range []string{owner, member, reader} {
				session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "synthetic-sharing", "127.0.0.1", auth.RefreshTokenTTL)
				if err != nil {
					t.Fatal(err)
				}
				tokens[user], err = validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
				if err != nil {
					t.Fatal(err)
				}
			}
			writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
			t.Cleanup(func() { _ = writer.Close() })
			hub := ws.NewHub(newTestLogger(), nil, validator, sessions.NewDBStore(h.db))
			hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(h.db))
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); hub.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			router, err := NewRouter(h.db, secret, newTestLogger(), WithJournal(writer), WithHub(hub), WithStoragePath(h.artifactStoragePath))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			mux := http.NewServeMux()
			mux.Handle("/api/", router)
			mux.HandleFunc("/ws", hub.HandleUpgrade)
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			request := func(user, method, path, body string, status int) []byte {
				t.Helper()
				sep := "?"
				if strings.Contains(path, "?") {
					sep = "&"
				}
				r, err := http.NewRequestWithContext(t.Context(), method, server.URL+path+sep+"workspace_id="+url.QueryEscape(workspace), strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				r.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tokens[user]})
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Origin", server.URL)
				response, err := server.Client().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != status {
					t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.StatusCode, status, raw)
				}
				return raw
			}
			pagePath := "/api/v1/pages/" + slug
			request(member, "GET", pagePath, "", 404)
			if kind != "legacy-panel" {
				request(owner, "POST", pagePath+"/public", `{}`, 400)
				var tokens int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_public_tokens WHERE page_id=?`, pageID).Scan(&tokens); err != nil || tokens != 0 {
					t.Fatalf("private output acquired public token: %d %v", tokens, err)
				}
			}
			if kind == "empty-human" {
				// These generic legacy paths validate nonempty panel specs
				// before either a write or a workspace invalidation.
				request(owner, "POST", pagePath+"/rollback", `{"to":1}`, 400)
				request(owner, "POST", "/api/v1/pages/import", `{"format":"crewship-page-bundle/v1","page":{"slug":"private-empty-import-canary","name":"Private empty import canary","panels":[]}}`, 400)
				var versions, imported int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_versions WHERE page_id=?`, pageID).Scan(&versions); err != nil || versions != 1 {
					t.Fatalf("refused rollback changed history: %d %v", versions, err)
				}
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id=? AND slug='private-empty-import-canary'`, workspace).Scan(&imported); err != nil || imported != 0 {
					t.Fatalf("refused empty import persisted page: %d %v", imported, err)
				}
			}
			var ticket struct {
				Token string `json:"token"`
			}
			if err = json.Unmarshal(request(member, "GET", "/api/v1/ws-token", "", 200), &ticket); err != nil {
				t.Fatal(err)
			}
			socket, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", "", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = socket.Close() })
			if err = websocket.JSON.Send(socket, map[string]string{"type": "auth", "token": ticket.Token}); err != nil {
				t.Fatal(err)
			}
			channel := "workspace:" + workspace
			if err = websocket.JSON.Send(socket, ws.ClientMessage{Type: "subscribe", Channel: channel}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for !hub.IsUserSubscribed(channel, member) {
				if time.Now().After(deadline) {
					t.Fatal("ungranted member could not subscribe to workspace")
				}
				time.Sleep(time.Millisecond)
			}
			private := kind != "legacy-panel"
			assertNoPrivate := func(raw []byte) {
				t.Helper()
				if !private {
					return
				}
				for _, canary := range []string{slug, title, folderSlugs[0], folderSlugs[1], "Private folder title canary", "Private source canary"} {
					if strings.Contains(string(raw), canary) {
						t.Errorf("workspace audience exposed private metadata %q: %s", canary, raw)
					}
				}
			}
			assertWS := func() {
				t.Helper()
				if err := socket.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var raw []byte
				if err := websocket.Message.Receive(socket, &raw); err != nil {
					t.Fatal(err)
				}
				assertNoPrivate(raw)
				var frame struct {
					Type, Channel string
					Payload       map[string]any
				}
				if err := json.Unmarshal(raw, &frame); err != nil {
					t.Fatal(err)
				}
				fields := 1
				if !private {
					fields = 2
				}
				if frame.Type != "page.updated" || frame.Channel != channel || frame.Payload["page_id"] != pageID || len(frame.Payload) != fields || (!private && frame.Payload["slug"] != slug) {
					t.Errorf("invalid workspace invalidation: %s", raw)
				}
			}
			request(owner, "PUT", pagePath+"/grants", fmt.Sprintf(`{"subject_type":"user","subject":%q,"level":"read"}`, reader), 200)
			request(reader, "GET", pagePath, "", 200)
			request(member, "GET", pagePath, "", 404)
			request(owner, "DELETE", pagePath+"/grants?subject_type=user&subject="+reader+"&level=read", "", 200)
			request(reader, "GET", pagePath, "", 404)
			for moveIndex, i := range []int{0, 1, 0} {
				folder := folderSlugs[i]
				var version, acl int64
				if err := h.db.QueryRow(`SELECT pages_version FROM pages WHERE id=?`, pageID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if err := h.db.QueryRow(`SELECT acl_version FROM page_folders WHERE id=?`, folderIDs[i]).Scan(&acl); err != nil {
					t.Fatal(err)
				}
				if moveIndex == 2 {
					request(owner, "POST", "/api/v1/page-folders/"+folder+"/pages:batch", fmt.Sprintf(`{"pages":[{"page":%q,"pages_version":%d}],"acl_version":%d}`, slug, version, acl), 200)
				} else {
					request(owner, "POST", "/api/v1/page-folders/"+folder+"/pages", fmt.Sprintf(`{"page":%q,"pages_version":%d,"acl_version":%d}`, slug, version, acl), 200)
				}
				assertWS()
				var actual string
				if err := h.db.QueryRow(`SELECT folder_id FROM pages WHERE id=?`, pageID).Scan(&actual); err != nil || actual != folderIDs[i] {
					t.Fatalf("move did not persist folder: %q %v", actual, err)
				}
				request(member, "GET", pagePath, "", 404)
			}
			var version int64
			if err := h.db.QueryRow(`SELECT pages_version FROM pages WHERE id=?`, pageID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			request(owner, "DELETE", "/api/v1/page-folders/"+folderSlugs[0]+"/pages/"+slug, fmt.Sprintf(`{"pages_version":%d}`, version), 200)
			assertWS()
			var empty bool
			if err := h.db.QueryRow(`SELECT folder_id IS NULL FROM pages WHERE id=?`, pageID).Scan(&empty); err != nil || !empty {
				t.Fatalf("removal did not clear folder: %t %v", empty, err)
			}
			request(owner, "GET", pagePath, "", 200)
			request(member, "GET", pagePath, "", 404)
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			type audit struct {
				ID, Summary string
				ActorID     string `json:"actor_id"`
				Payload     map[string]any
			}
			for _, typ := range []string{"page.grant_added", "page.grant_removed", "page.folder_membership_changed"} {
				raw := request(member, "GET", "/api/v1/journal?entry_type="+typ, "", 200)
				assertNoPrivate(raw)
				var list struct{ Entries []audit }
				if err := json.Unmarshal(raw, &list); err != nil {
					t.Fatal(err)
				}
				count := 1
				if typ == "page.folder_membership_changed" {
					count = 4
				}
				if len(list.Entries) != count {
					t.Fatalf("%s audit count=%d want=%d: %s", typ, len(list.Entries), count, raw)
				}
				for _, entry := range list.Entries {
					if entry.ActorID != owner || entry.Payload["actor_user_id"] != owner || entry.Payload["page_id"] != pageID {
						t.Errorf("lost audit identity: %+v", entry)
					}
					if strings.HasPrefix(typ, "page.grant_") {
						if entry.Payload["subject_id"] != reader || entry.Payload["subject_type"] != "user" || entry.Payload["level"] != "read" {
							t.Errorf("lost grant audit: %+v", entry)
						}
					} else if private {
						expectedFolders := map[float64][2]string{1: {"", folderIDs[0]}, 2: {folderIDs[0], folderIDs[1]}, 3: {folderIDs[1], folderIDs[0]}, 4: {folderIDs[0], ""}}
						version, ok := entry.Payload["pages_version"].(float64)
						expected, exists := expectedFolders[version]
						if !ok || !exists || entry.Payload["from_folder_id"] != expected[0] || entry.Payload["to_folder_id"] != expected[1] || (version == 4 && entry.Payload["op"] != "removed") || (version != 4 && entry.Payload["op"] != "moved") {
							t.Errorf("lost folder audit identity/version: %+v", entry)
						}
					}
					if private {
						for _, key := range []string{"page", "subject", "from", "to", "panels", "fingerprint"} {
							if _, ok := entry.Payload[key]; ok {
								t.Errorf("private audit retained field %s: %+v", key, entry)
							}
						}
					} else if entry.Payload["page"] != slug || !strings.Contains(entry.Summary, slug) {
						t.Errorf("legacy audit changed: %+v", entry)
					}
					assertNoPrivate(request(member, "GET", "/api/v1/journal/"+entry.ID, "", 200))
				}
			}
		})
	}
}
