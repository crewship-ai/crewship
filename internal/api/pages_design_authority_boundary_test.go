package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/design"
	"github.com/gorilla/websocket"
)

type designAuthorityFixture struct {
	h                      *PageHandler
	workspace, owner, page string
	tokens                 map[string]string
}

func newDesignAuthorityFixture(t *testing.T) *designAuthorityFixture {
	t.Helper()
	h, workspace, owner, _ := designFixture(t)
	f := &designAuthorityFixture{h: h, workspace: workspace, owner: owner, tokens: map[string]string{}}
	if w := f.call(t, owner, "create", `{"slug":"board","name":"Authority boundary"}`); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='board'`, workspace).Scan(&f.page); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *designAuthorityFixture) token(t *testing.T, user string) string {
	t.Helper()
	if token := f.tokens[user]; token != "" {
		return token
	}
	token := mintTokenFor(t, f.h.db, user, "design-authority-"+user)
	f.tokens[user] = token
	return token
}
func (f *designAuthorityFixture) request(t *testing.T, user, method, suffix, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1/pages/board/design"+suffix, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.token(t, user))
	r.Header.Set("Content-Type", "application/json")
	ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), f.workspace, "MEMBER")
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	r = r.WithContext(ctx)
	r.SetPathValue("slug", "board")
	return r
}
func (f *designAuthorityFixture) call(t *testing.T, user, operation, body string) *httptest.ResponseRecorder {
	t.Helper()
	method := http.MethodPost
	if operation == "get" || operation == "versions" || operation == "comments" {
		method = http.MethodGet
	}
	r := f.request(t, user, method, "", body)
	w := httptest.NewRecorder()
	handlers := map[string]http.HandlerFunc{"create": f.h.CreateDesign, "get": f.h.GetDesign, "apply": f.h.ApplyDesign, "snapshot": f.h.SnapshotDesign, "restore": f.h.RestoreDesign, "share": f.h.ShareDesign, "agent": f.h.ConnectDesignAgent, "versions": f.h.DesignVersions, "comment": f.h.CreateDesignComment, "comments": f.h.GetDesignComments}
	handler := handlers[operation]
	if handler == nil {
		t.Fatalf("unknown operation %s", operation)
	}
	handler(w, r)
	return w
}
func (f *designAuthorityFixture) addUser(t *testing.T, user string) {
	t.Helper()
	mustExec(t, f.h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, user, user+"@example.test", user)
	mustExec(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), f.workspace, user)
}

// Use the production grant route, including its authenticated mutation wrapper.
func (f *designAuthorityFixture) grant(t *testing.T, user, level string) *httptest.ResponseRecorder {
	t.Helper()
	router, err := NewRouter(f.h.db, "synthetic-design-authority-grant-secret-long-enough", newTestLogger(), WithInternalToken("synthetic-design-authority-internal-master-long-enough"))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	raw, _ := json.Marshal(map[string]string{"subject_type": "user", "subject": user, "level": level})
	r := httptest.NewRequest(http.MethodPut, "/api/v1/pages/board/grants", strings.NewReader(string(raw)))
	r.Header.Set("Authorization", "Bearer "+f.token(t, f.owner))
	r.Header.Set("X-Workspace-ID", f.workspace)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func (f *designAuthorityFixture) revision(t *testing.T) int64 {
	t.Helper()
	var revision int64
	if err := f.h.db.QueryRow(`SELECT revision FROM artifact_design_documents WHERE workspace_id=? AND page_id=?`, f.workspace, f.page).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}
func (f *designAuthorityFixture) versions(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.h.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE workspace_id=? AND page_id=?`, f.workspace, f.page).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func (f *designAuthorityFixture) dial(t *testing.T, user string) *websocket.Conn {
	t.Helper()
	token := f.token(t, user)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), f.workspace, "MEMBER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "board")
		f.h.DesignWebsocket(w, r)
	}))
	t.Cleanup(server.Close)
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/pages/board/design/ws", http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil {
		if response != nil {
			t.Fatalf("socket admission %d: %v", response.StatusCode, err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err = conn.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	for {
		typ, raw, err := conn.ReadMessage()
		if err != nil || typ != websocket.BinaryMessage {
			t.Fatalf("initial socket sync %d: %v", typ, err)
		}
		kind, sub, err := designSyncKind(raw)
		if err != nil {
			t.Fatal(err)
		}
		if kind == 0 && sub == 1 {
			break
		}
	}
	return conn
}
func designAuthorityClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	_, _, err := conn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("old admission delivered content or stayed open: %v", err)
	}
}
func (f *designAuthorityFixture) regrantSameTransaction(t *testing.T, user, level string) {
	t.Helper()
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id=? AND level=?`, f.page, user, level); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,?,?)`, f.page, user, level, f.owner); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestDesignWebsocketGrantABARejectsOldWriteAndReadAdmissions(t *testing.T) {
	for _, level := range []string{"write", "read"} {
		t.Run(level, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			user := "design-aba-" + level
			f.addUser(t, user)
			if w := f.grant(t, user, level); w.Code != 200 {
				t.Fatalf("grant %d %s", w.Code, w.Body)
			}
			old := f.dial(t, user)
			revision, versions := f.revision(t), f.versions(t)
			f.regrantSameTransaction(t, user, level)
			if level == "write" {
				update, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"old-admission-write","type":"rect","x":0,"y":0,"width":10,"height":10}}]`)})
				if err != nil {
					t.Fatal(err)
				}
				if err = old.WriteMessage(websocket.BinaryMessage, designUpdateFrame(update.Update)); err != nil {
					t.Fatal(err)
				}
				designAuthorityClosed(t, old)
				if f.revision(t) != revision || f.versions(t) != versions {
					t.Fatal("old write admission changed live state or history after grant ABA")
				}
			} else {
				if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"post-aba-content","type":"text","x":0,"y":0,"width":200,"height":30,"text":"Only fresh admissions may receive this"}}]}`); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
				designAuthorityClosed(t, old)
			}
			fresh := f.dial(t, user)
			if level == "write" {
				update, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"fresh-admission-write","type":"rect","x":0,"y":0,"width":10,"height":10}}]`)})
				if err != nil {
					t.Fatal(err)
				}
				if err = fresh.WriteMessage(websocket.BinaryMessage, designUpdateFrame(update.Update)); err != nil {
					t.Fatal(err)
				}
				if err = fresh.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err != nil {
					t.Fatal(err)
				}
				_ = fresh.SetReadDeadline(time.Now().Add(6 * time.Second))
				for {
					_, raw, err := fresh.ReadMessage()
					if err != nil {
						t.Fatal(err)
					}
					kind, sub, err := designSyncKind(raw)
					if err != nil {
						t.Fatal(err)
					}
					if kind == 0 && sub == 1 {
						break
					}
				}
				if f.revision(t) <= revision {
					t.Fatal("fresh credential admission could not write after regrant")
				}
			}
		})
	}
}

func TestDesignCommentGrantABARollsBackFeedbackAndTask(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	user := "design-comment-aba"
	f.addUser(t, user)
	if w := f.grant(t, user, "write"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"saved-title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"Saved title"}}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	bodySQL := fmt.Sprintf(`DELETE FROM page_grants WHERE page_id='%s' AND subject_type='user' AND subject_id='%s' AND level='write'; INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('%s','user','%s','write','%s');`, f.page, user, f.page, user, f.owner)
	mustExec(t, f.h.db, `CREATE TRIGGER design_comment_aba AFTER INSERT ON artifact_project_comments BEGIN `+bodySQL+` END`)
	versions, revision := f.versions(t), f.revision(t)
	w := f.call(t, user, "comment", `{"version":2,"path":"design.json","selector":"saved-title","quote":"Saved title","text":"Must not survive revoked admission","send_to_agent":true}`)
	if w.Code != 403 && w.Code != 404 {
		t.Fatalf("same final ACL refreshed original comment admission: %d %s", w.Code, w.Body)
	}
	for _, table := range []string{"artifact_project_comments", "artifact_project_comment_tasks"} {
		var n int
		if err := f.h.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE page_id=?`, f.page).Scan(&n); err != nil || n != 0 {
			t.Fatalf("revoked comment persisted %s rows %d: %v", table, n, err)
		}
	}
	if f.versions(t) != versions || f.revision(t) != revision {
		t.Fatal("denied comment changed canvas or versions")
	}
}

func (f *designAuthorityFixture) liveOnly(t *testing.T, operations string) {
	t.Helper()
	key := artifacts.VersionKey{WorkspaceID: f.workspace, PageID: f.page}
	room := f.h.designRoom(key)
	room.mu.Lock()
	defer room.mu.Unlock()
	request := f.request(t, f.owner, http.MethodPost, "/ws", "")
	if _, err := f.h.designApplyLocked(request, key, design.Request{Operations: json.RawMessage(operations)}); err != nil {
		t.Fatal(err)
	}
}
func TestDesignCommentAnchorsImmutableVersionInsteadOfUnsavedLiveDocument(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	if w := f.call(t, f.owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"saved-title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"Saved title"}}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	f.liveOnly(t, `[{"op":"upsert","node":{"id":"saved-title","text":"Unsaved live title"}},{"op":"upsert","node":{"id":"live-only-node","type":"text","x":0,"y":40,"width":200,"height":30,"text":"Not in saved version"}}]`)
	versions, revision := f.versions(t), f.revision(t)
	for _, body := range []string{
		`{"version":2,"path":"design.json","selector":"saved-title","quote":"Unsaved live title","text":"Wrong version bytes"}`,
		`{"version":2,"path":"design.json","selector":"live-only-node","quote":"Not in saved version","text":"Wrong version node"}`,
	} {
		if w := f.call(t, f.owner, "comment", body); w.Code != 409 {
			t.Fatalf("live bytes masqueraded as saved version: %d %s", w.Code, w.Body)
		}
	}
	if w := f.call(t, f.owner, "comment", `{"version":2,"path":"design.json","selector":"saved-title","quote":"Saved title","text":"Exactly the saved version"}`); w.Code != 201 {
		t.Fatalf("immutable saved anchor rejected after live edit: %d %s", w.Code, w.Body)
	}
	if f.versions(t) != versions || f.revision(t) != revision {
		t.Fatal("feedback silently saved or rewrote live canvas")
	}
}

func TestDesignCanonicalCommentGrantIsFailClosedOrFeedbackOnly(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	user := "design-canonical-comment"
	f.addUser(t, user)
	var schema string
	if err := f.h.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='page_grants'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	installed := strings.Contains(schema, "'comment'")
	granted := f.grant(t, user, "comment")
	if !installed {
		if granted.Code != 400 && granted.Code != 403 {
			t.Fatalf("absent Comment schema must reject grant: %d %s", granted.Code, granted.Body)
		}
		var count int
		if err := f.h.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id=? AND subject_id=?`, f.page, user).Scan(&count); err != nil || count != 0 {
			t.Fatalf("absent Comment schema retained a grant %d %v", count, err)
		}
		t.Log("canonical Comment schema absent: real grant API refusal verified; full Comment acceptance is not exercised")
		return
	}
	if granted.Code != 200 {
		t.Fatalf("canonical real Comment grant %d %s", granted.Code, granted.Body)
	}
	for _, step := range []struct{ op, body string }{
		{"apply", `{"operations":[{"op":"upsert","node":{"id":"saved-title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"Shared saved title"}}]}`},
		{"share", `{"version":2}`},
		{"apply", `{"operations":[{"op":"upsert","node":{"id":"saved-title","text":"Latest saved title"}}]}`},
	} {
		if w := f.call(t, f.owner, step.op, step.body); w.Code != 200 {
			t.Fatalf("setup %s %d %s", step.op, w.Code, w.Body)
		}
	}
	f.liveOnly(t, `[{"op":"upsert","node":{"id":"live-node","type":"rect","x":0,"y":80,"width":100,"height":50}}]`)
	get := f.call(t, user, "get", "")
	var view struct {
		Nodes      []json.RawMessage `json:"nodes"`
		CanEdit    bool              `json:"can_edit"`
		CanComment bool              `json:"can_comment"`
	}
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &view) != nil || view.CanEdit || !view.CanComment || len(view.Nodes) != 2 {
		t.Fatalf("Comment live projection %d %s", get.Code, get.Body)
	}
	versions, revision := f.versions(t), f.revision(t)
	comment := `{"version":3,"path":"design.json","selector":"saved-title","quote":"Latest saved title","text":"Feedback without content authority"}`
	if w := f.call(t, user, "comment", comment); w.Code != 201 {
		t.Fatalf("Comment feedback %d %s", w.Code, w.Body)
	}
	for _, step := range []struct{ op, body string }{
		{"apply", `{"operations":[{"op":"delete","id":"saved-title"}]}`}, {"snapshot", `{}`}, {"restore", `{"version":2}`}, {"share", `{"version":3}`}, {"agent", `{"chat_id":"cannot-create-owner-mandate"}`},
		{"comment", `{"version":3,"path":"design.json","selector":"saved-title","quote":"Latest saved title","text":"Cannot dispatch owner agent","send_to_agent":true}`},
	} {
		if w := f.call(t, user, step.op, step.body); w.Code != 403 {
			t.Errorf("Comment gained %s authority: %d %s", step.op, w.Code, w.Body)
		}
	}
	if f.versions(t) != versions || f.revision(t) != revision {
		t.Fatal("Comment feedback/mutation attempts changed live canvas or snapshot history")
	}
	var intents int
	if err := f.h.db.QueryRow(`SELECT count(*) FROM artifact_project_message_intents WHERE page_id=?`, f.page).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("Comment minted agent mandate %d %v", intents, err)
	}
	history := f.call(t, user, "versions", "")
	var listed struct {
		Versions []struct {
			Version int64 `json:"version"`
		} `json:"versions"`
	}
	if history.Code != 200 || json.Unmarshal(history.Body.Bytes(), &listed) != nil || len(listed.Versions) != 1 || listed.Versions[0].Version != 2 {
		t.Fatalf("Comment history escaped shared pin: %d %s", history.Code, history.Body)
	}
	request := f.request(t, user, http.MethodGet, "?version=3", "")
	refused := httptest.NewRecorder()
	f.h.GetDesign(refused, request)
	if refused.Code != 404 {
		t.Fatalf("Comment selected unshared retained history: %d %s", refused.Code, refused.Body)
	}
	// Read-only live synchronization remains available, but a document update
	// from the same Comment socket is still a write and must be denied.
	socket := f.dial(t, user)
	update, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"forbidden-comment-write","type":"rect","x":0,"y":0,"width":10,"height":10}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = socket.WriteMessage(websocket.BinaryMessage, designUpdateFrame(update.Update)); err != nil {
		t.Fatal(err)
	}
	designAuthorityClosed(t, socket)
	if f.revision(t) != revision {
		t.Fatal("Comment socket mutated live state")
	}
	var actor string
	if err = f.h.db.QueryRow(`SELECT author FROM artifact_project_comments WHERE page_id=?`, f.page).Scan(&actor); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if actor != user {
		t.Fatalf("feedback lost actual Comment author: %q", actor)
	}
}

func TestDesignRenewalAdmissionRejectsStaleBrowserAuthority(t *testing.T) {
	f := newDesignAuthorityFixture(t)
	f.addUser(t, "editor")
	if w := f.grant(t, "editor", "write"); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	token := f.token(t, "editor")
	authority := func() string {
		w := f.call(t, "editor", "get", "")
		var view struct {
			AuthorityRevision string `json:"authority_revision"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.AuthorityRevision == "" {
			t.Fatalf("metadata authority %d %s", w.Code, w.Body)
		}
		return view.AuthorityRevision
	}
	previous := authority()
	f.regrantSameTransaction(t, "editor", "write")
	current := authority()
	if previous == current {
		t.Fatal("regrant did not advance authority")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: "editor"}), f.workspace, "MEMBER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "board")
		f.h.DesignWebsocket(w, r)
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/pages/board/design/ws?authority_revision="
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	stale, response, err := websocket.DefaultDialer.Dial(endpoint+fmt.Sprint(previous), headers)
	if stale != nil {
		_ = stale.Close()
	}
	if err == nil || response == nil || response.StatusCode != 403 {
		t.Fatalf("stale admission: response=%v error=%v", response, err)
	}
	conn, response, err := websocket.DefaultDialer.Dial(endpoint+fmt.Sprint(current), headers)
	if err != nil {
		t.Fatalf("fresh admission: response=%v error=%v", response, err)
	}
	_ = conn.Close()
}
