package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func chatLinksRouter(t *testing.T, h *PageHandler, workspace, user string, enabled bool) (*Router, func(string) *httptest.ResponseRecorder) {
	t.Helper()
	const token = "crewship_cli_chat_document_links_test"
	mustExec(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('links-token',?,'links',?,'STANDARD','["pages:read"]')`, user, hashStandard(token))
	var options []RouterOption
	if enabled {
		dir, err := os.MkdirTemp("", "chat-links-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		secret := filepath.Join(dir, "secret")
		if err := os.WriteFile(secret, []byte(strings.Repeat("links-service-secret-", 3)), 0600); err != nil {
			t.Fatal(err)
		}
		options = append(options, WithDocumentAuthority(filepath.Join(dir, "authority.sock"), secret), WithDocumentService(filepath.Join(dir, "unserved.sock")))
	}
	router, err := NewRouter(h.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	return router, func(chat string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/chats/"+chat+"/links?workspace_id="+workspace, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
}

func TestChatDocumentLinksLatestScopedAndAuthorized(t *testing.T) {
	h, workspace, owner, saved := documentResultsFixture(t)
	_, read := chatLinksRouter(t, h, workspace, owner, true)
	h.db.SetMaxOpenConns(1)
	// Creation revision 0 is not a pin to the current revision. Latest is
	// explicit and does not require reading an engine socket or content bytes.
	mustExec(t, h.db, `UPDATE documents SET head_rev=7 WHERE page_id=?`, saved.PageID)
	w := read("principal-chat")
	creationExpect(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private links are cacheable")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 2 || string(body["scope"]) != `"latest_native_documents"` {
		t.Fatalf("truthful closed scope: %s err=%v", w.Body, err)
	}
	var links []map[string]any
	if err := json.Unmarshal(body["links"], &links); err != nil || len(links) != 1 {
		t.Fatalf("linked saved Document: %s err=%v", w.Body, err)
	}
	link := links[0]
	if len(link) != 6 || link["page_id"] != saved.PageID || link["name"] != saved.Title || link["kind"] != "artifact" || link["accessible"] != true || link["mode"] != "latest" || link["link_id"] != "document:"+saved.PageID {
		t.Fatalf("closed latest link DTO: %#v", link)
	}
	for _, forbidden := range []string{"revision", "checkpoint_id", "run_id", "on_behalf_of_user_id", "snapshot", "content"} {
		if _, ok := link[forbidden]; ok {
			t.Errorf("link discloses unsupported field %s", forbidden)
		}
	}
	// A real Document without this durable conversation provenance is omitted.
	mustExec(t, h.db, `UPDATE document_creations SET source_chat_id=NULL`)
	w = read("principal-chat")
	creationExpect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"links":[]`) || strings.Contains(w.Body.String(), saved.PageID) {
		t.Fatalf("unlinked output leaked: %s", w.Body)
	}
}

func TestChatDocumentLinksCurrentAuthorityAndGating(t *testing.T) {
	for _, change := range []string{"private-page", "revoked-token", "narrow-scope", "membership-removed", "restricted", "missing-chat", "disabled", "disabled-hidden-chat", "sql-failure"} {
		t.Run(change, func(t *testing.T) {
			h, workspace, owner, saved := documentResultsFixture(t)
			_, read := chatLinksRouter(t, h, workspace, owner, !strings.HasPrefix(change, "disabled"))
			chat, want := "principal-chat", 404
			switch change {
			case "private-page":
				pagesSeedUser(t, h, workspace, "links-new-owner", "links-new@example.invalid", "MEMBER")
				mustExec(t, h.db, `UPDATE pages SET owner_user_id='links-new-owner',private_audience=1 WHERE id=?`, saved.PageID)
				want = 200
			case "revoked-token":
				mustExec(t, h.db, `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE id='links-token'`)
				want = 401
			case "narrow-scope":
				mustExec(t, h.db, `UPDATE cli_tokens SET scopes='["agents:read"]' WHERE id='links-token'`)
				want = 403
			case "membership-removed":
				mustExec(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, owner)
				want = 403
			case "restricted":
				mustExec(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?`, owner)
			case "missing-chat", "disabled-hidden-chat":
				chat = "not-a-chat"
			case "disabled":
				want = 503
			case "sql-failure":
				mustExec(t, h.db, `ALTER TABLE document_creations RENAME TO unavailable_creations`)
				want = 500
			}
			w := read(chat)
			creationExpect(t, w, want)
			if strings.Contains(w.Body.String(), saved.PageID) || strings.Contains(w.Body.String(), saved.Title) {
				t.Fatalf("denial disclosed Document metadata: %s", w.Body)
			}
			if change == "private-page" && !strings.Contains(w.Body.String(), `"links":[]`) {
				t.Fatalf("inaccessible output must be omitted without placeholder/count: %s", w.Body)
			}
		})
	}
}
