package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestDiscussionPaginationCursorScope(t *testing.T) {
	r := httptest.NewRequest("GET", "/?status=resolved&limit=25", nil)
	p, err := parseDiscussionPage(r, "ws", "page", "artifact", 2)
	if err != nil {
		t.Fatal(err)
	}
	if p.Limit != 25 || p.Status != "resolved" {
		t.Fatalf("page=%+v", p)
	}
	cursor := p.cursor("2026-10-15T10:00:00Z", "thread", 0)
	for _, scope := range []struct {
		ws, page, kind string
		revision       int64
	}{{"other", "page", "artifact", 2}, {"ws", "other", "artifact", 2}, {"ws", "page", "board", 2}, {"ws", "page", "artifact", 3}} {
		request := httptest.NewRequest("GET", "/?status=resolved&cursor="+cursor, nil)
		if _, err := parseDiscussionPage(request, scope.ws, scope.page, scope.kind, scope.revision); err == nil {
			t.Fatalf("accepted foreign scope %+v", scope)
		}
	}
	request := httptest.NewRequest("GET", "/?status=resolved&cursor="+cursor, nil)
	decoded, err := parseDiscussionPage(request, "ws", "page", "artifact", 2)
	if err != nil || decoded.ID != "thread" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for _, query := range []string{"status=hidden", "limit=0", "limit=101", "cursor=garbage", "status=open&cursor=" + cursor} {
		if _, err := parseDiscussionPage(httptest.NewRequest("GET", "/?"+query, nil), "ws", "page", "artifact", 2); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
}

func TestDiscussionPaginationRetainedHistory(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _, ws, user, page := artifactFixture(t)
			anchor := `{"schema_version":1,"type":"document"}`
			if kind == "artifact" {
				artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Original</h1>", "first", 0), 201)
			} else {
				artifactExpect(t, boardCall(t, h, ws, user, "OWNER", "report", "init", `{"mutation_id":"init"}`), 200)
				anchor = `{"schema_version":1,"type":"board","board_seq":0}`
			}
			for i := 0; i < 205; i++ {
				id := fmt.Sprintf("thread-%03d", i)
				query := "INSERT INTO page_" + kind + "_threads(id,page_id,anchor_json,status,created_by_user_id,created_at,updated_at"
				values := ") VALUES(?,?,?,'resolved',?,'2026-10-15T10:00:00Z','2026-10-15T10:00:00Z'"
				if kind == "artifact" {
					query += ",revision"
					values += ",1"
				}
				if _, err := h.db.Exec(query+values+")", id, page, anchor, user); err != nil {
					t.Fatal(err)
				}
			}
			call := func(query string) *httptest.ResponseRecorder {
				r := pagesGrantRequest(t, "GET", "/?"+query, ws, user, "OWNER", "")
				r.SetPathValue("slug", "report")
				w := httptest.NewRecorder()
				if kind == "artifact" {
					h.ListArtifactThreads(w, r)
				} else {
					h.ListBoardThreads(w, r)
				}
				return w
			}
			cursor := ""
			seen := map[string]bool{}
			pages := 0
			for {
				w := call("status=resolved&limit=73&cursor=" + cursor)
				artifactExpect(t, w, 200)
				var result struct {
					Threads []struct {
						ID       string          `json:"id"`
						Anchor   json.RawMessage `json:"anchor"`
						Revision int64           `json:"revision"`
					} `json:"threads"`
					Next string `json:"next_cursor"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				for _, thread := range result.Threads {
					if seen[thread.ID] {
						t.Fatalf("duplicate %s", thread.ID)
					}
					seen[thread.ID] = true
					if string(thread.Anchor) != anchor {
						t.Fatalf("anchor moved: %s", thread.Anchor)
					}
					if kind == "artifact" && thread.Revision != 1 {
						t.Fatal("revision moved")
					}
				}
				pages++
				if result.Next == "" {
					break
				}
				cursor = result.Next
				if pages > 4 {
					t.Fatal("pagination did not terminate")
				}
			}
			if len(seen) != 205 || pages != 3 {
				t.Fatalf("seen=%d pages=%d", len(seen), pages)
			}
			// A cursor scoped to a different Page is refused by the real handler.
			foreign := discussionPage{Workspace: ws, Page: "foreign-page", Kind: kind, Status: "resolved"}
			if kind == "artifact" {
				foreign.Revision = 0
			}
			artifactExpect(t, call("status=resolved&cursor="+foreign.cursor("2026-10-15T10:00:00Z", "thread-100", 0)), 400)
			artifactExpect(t, call("status=open&cursor="+cursor), 400)
			openPage := call("status=open")
			artifactExpect(t, openPage, 200)
			var empty struct {
				Threads []json.RawMessage `json:"threads"`
			}
			if err := json.Unmarshal(openPage.Body.Bytes(), &empty); err != nil || len(empty.Threads) != 0 {
				t.Fatalf("open filter: %s", openPage.Body.String())
			}
			if kind == "artifact" {
				artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Second</h1>", "second", 1), 201)
				artifactExpect(t, call("status=resolved&revision=2&cursor="+cursor), 400)
				filtered := call("status=resolved&revision=2")
				artifactExpect(t, filtered, 200)
				if err := json.Unmarshal(filtered.Body.Bytes(), &empty); err != nil || len(empty.Threads) != 0 {
					t.Fatalf("revision filter: %s", filtered.Body.String())
				}
			}
			// Open work remains first without hiding the retained resolved history.
			if _, err := h.db.Exec("UPDATE page_" + kind + "_threads SET status='open' WHERE id='thread-000'"); err != nil {
				t.Fatal(err)
			}
			mixed := call("status=all&limit=1")
			artifactExpect(t, mixed, 200)
			var first struct {
				Threads []struct {
					ID string `json:"id"`
				} `json:"threads"`
				Next string `json:"next_cursor"`
			}
			if err := json.Unmarshal(mixed.Body.Bytes(), &first); err != nil || len(first.Threads) != 1 || first.Threads[0].ID != "thread-000" || first.Next == "" {
				t.Fatalf("open ordering: %s", mixed.Body.String())
			}
			following := call("status=all&limit=100&cursor=" + first.Next)
			artifactExpect(t, following, 200)
			if err := json.Unmarshal(following.Body.Bytes(), &first); err != nil || len(first.Threads) != 100 || first.Threads[0].ID != "thread-204" {
				t.Fatalf("resolved continuation: %s", following.Body.String())
			}
			// Revoking membership after one page must defeat an otherwise valid cursor.
			if _, err := h.db.Exec("DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?", ws, user); err != nil {
				t.Fatal(err)
			}
			w := call("status=resolved&limit=73&cursor=" + cursor)
			if w.Code < 400 {
				t.Fatalf("revoked reader received %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
