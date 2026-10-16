package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/slides"
)

func TestSlidesCommentsHTTPRolesRevocationAndImmutablePage(t *testing.T) {
	f := newInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "comment-page", "comment-page", "Slides", "boss", "")
	deck := slideTestDeck(t, "comment-page")
	base := "/api/v1/pages/comment-page/slides"
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "init", Deck: deck})), 200, "initialize")
	seedInstanceUser(t, f.db, "commenter", "commenter@example.test", "ws-old", "MEMBER")
	token := mintTokenFor(t, f.db, "commenter", "comment-session")
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('comment-page','user','commenter','comment','boss')`)
	create := slides.CreateCommentRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 1, MutationID: "thread_one", Anchor: slides.CommentAnchor{AnchorSchemaVersion: 1, Kind: "slide", SlideID: deck.SlideOrder[0], Revision: 1}, Body: "Český komentář"}
	wantCode(t, f.slideDo(token, "POST", base+"/comments", slideJSON(t, create)), 200, "comment grant creates")
	create.MutationID = "thread_two"
	wantCode(t, f.slideDo(f.boss, "POST", base+"/comments", slideJSON(t, create)), 200, "second thread")
	result := f.slideDo(token, "GET", base+"/comments?limit=1", "")
	wantCode(t, result, 200, "comment list")
	var page SlidesCommentThreadsResponse
	if err := json.Unmarshal(result.Body.Bytes(), &page); err != nil || !page.CanComment || page.CanResolve || !page.HasMore {
		t.Fatal(result.Body.String(), err)
	}
	reply := slides.ReplyCommentRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "reply", IfVersion: 1, Body: "Reply"}
	wantCode(t, f.slideDo(token, "POST", base+"/comments/thread_one/replies", slideJSON(t, reply)), 200, "comment grant replies")
	resolve := slides.ResolveCommentRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "resolve", IfVersion: 2, Resolved: true}
	wantCode(t, f.slideDo(token, "POST", base+"/comments/thread_one/resolve", slideJSON(t, resolve)), 403, "comment grant cannot resolve")
	wantCode(t, f.slideDo(f.boss, "POST", base+"/comments/thread_one/resolve", slideJSON(t, resolve)), 200, "writer resolves")
	mustExec(t, f.db, `UPDATE page_grants SET level='read' WHERE page_id='comment-page'`)
	create.MutationID = "reader_create"
	wantCode(t, f.slideDo(token, "POST", base+"/comments", slideJSON(t, create)), 403, "reader cannot comment")
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='comment-page'`)
	wantCode(t, f.slideDo(token, "GET", base+"/comments?cursor="+url.QueryEscape(page.NextCursor), ""), 403, "revocation before page two")
	wantCode(t, f.slideDo(token, "POST", base+"/comments/thread_one/replies", slideJSON(t, reply)), 403, "revoked receipt replay")
	// Comment history is retained independently of the author's account.
	mustExec(t, f.db, `DELETE FROM cli_tokens WHERE user_id='commenter'`)
	mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id='commenter'`)
	mustExec(t, f.db, `DELETE FROM users WHERE id='commenter'`)
	retained := f.slideDo(f.boss, "GET", base+"/comments/thread_one/history", "")
	wantCode(t, retained, 200, "history survives author deletion")
	var retainedHistory SlidesCommentHistoryResponse
	if err := json.Unmarshal(retained.Body.Bytes(), &retainedHistory); err != nil || len(retainedHistory.Items) != 3 || retainedHistory.Items[0].Body != "Český komentář" || retainedHistory.Items[0].Actor != "user:commenter" {
		t.Fatal("author deletion changed retained history", retained.Body.String(), err)
	}
	mustExec(t, f.db, `DELETE FROM pages WHERE id='comment-page'`)
	wmSeedPage(t, f.db, "ws-old", "comment-page-new", "comment-page", "Slides", "boss", "")
	deck.DeckID = "comment-page-new"
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "new_init", Deck: deck})), 200, "recreate")
	create.MutationID = "stale_page"
	wantCode(t, f.slideDo(f.boss, "POST", base+"/comments", slideJSON(t, create)), 409, "old Page binding")
}

func TestSlidesCommentsRejectRunAuthorityForEveryPermission(t *testing.T) {
	h := &slideHTTP{}
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, &artifactRunAuthority{}))
	for _, permission := range []slidePermission{slideRead, slideComment, slideWrite} {
		if _, err := h.commentAuthority(r, slides.Key{}, permission)(r.Context(), (*sql.Tx)(nil)); err == nil {
			t.Fatalf("run inherited comment permission %v", permission)
		}
	}
}

func TestSlidesCommentsExactCredentialRevocationDuringBodyAndReplay(t *testing.T) {
	for _, credential := range []string{"cli", "session"} {
		for _, replay := range []bool{false, true} {
			name := credential + "/new"
			if replay {
				name = credential + "/receipt"
			}
			t.Run(name, func(t *testing.T) {
				f := newInstanceFixture(t)
				wmSeedPage(t, f.db, "ws-old", "credential-page", "credential-page", "Slides", "boss", "")
				deck := slideTestDeck(t, "credential-page")
				base := "/api/v1/pages/credential-page/slides"
				wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, slides.InitializeRequest{PageID: deck.DeckID, SchemaVersion: 1, MutationID: "init", Deck: deck})), 200, "initialize")
				token := f.boss
				revoke := func() {
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(token))
				}
				if credential == "session" {
					now := time.Now().UTC()
					mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,created_at,expires_at,last_used_at) VALUES('comment-session','boss',?,?,?)`, now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
					validator, e := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
					if e != nil {
						t.Fatal(e)
					}
					token, e = validator.IssueAccessToken("boss", "comment-session", "Boss", "boss@ex.com")
					if e != nil {
						t.Fatal(e)
					}
					revoke = func() {
						mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id='comment-session'`)
					}
				}
				request := slides.CreateCommentRequest{PageID: deck.DeckID, SchemaVersion: 1, BaseRev: 1, MutationID: "credential_comment", Anchor: slides.CommentAnchor{AnchorSchemaVersion: 1, Kind: "slide", SlideID: deck.SlideOrder[0], Revision: 1}, Body: "Must remain private"}
				body := slideJSON(t, request)
				if replay {
					wantCode(t, f.slideDo(token, "POST", base+"/comments", body), 200, "initial acknowledged receipt")
				}
				r := httptest.NewRequest("POST", base+"/comments?workspace_id=ws-old", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Body = &artifactRevokingBody{reader: strings.NewReader(body), revoke: revoke}
				rr := httptest.NewRecorder()
				f.r.ServeHTTP(rr, r)
				wantCode(t, rr, 403, "exact credential revoked during body")
				var count int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_comment_events`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				expected := 0
				if replay {
					expected = 1
				}
				if count != expected {
					t.Fatal("revocation changed retained history", count)
				}
				wantCode(t, f.slideDo(token, "POST", base+"/comments", body), 401, "revoked credential retry")
			})
		}
	}
}
