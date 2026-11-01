package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/design"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/gorilla/websocket"
)

func TestDesignClientQueueNeverBlocksRoom(t *testing.T) {
	client := &designClient{out: make(chan []byte, 1)}
	if err := client.enqueue([]byte{1}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.enqueue([]byte{2}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("full queue must fail")
		}
	case <-time.After(time.Second):
		t.Fatal("full queue blocks room")
	}
}

// This is a real typed chat admission and original-run tool round trip. No
// browser/WebSocket exists: an agent must still read, edit and version a canvas.
func TestDesignTypedRunCanEditOfflineOnlyItsSelectedCanvas(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "false")
	f := artifactProjectOriginalRunFixture(t)
	const master = "synthetic-design-reviewed-master"
	f.pages.artifactMasterToken = master
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "design-reviewed-owner")
	human := func(slug, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := pagesRequest(t, "POST", "/api/v1/pages/"+slug+"/design", f.a.WorkspaceID, f.a.HumanUserID, "OWNER", body)
		r.Header.Set("Authorization", "Bearer "+humanToken)
		r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
		r.SetPathValue("slug", slug)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	for _, slug := range []string{"review-design-a", "review-design-b"} {
		if w := human(slug, fmt.Sprintf(`{"slug":%q,"name":"Reviewed Design"}`, slug), f.pages.CreateDesign); w.Code != 201 {
			t.Fatalf("create %d %s", w.Code, w.Body)
		}
	}
	var page string
	if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='review-design-a'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	intent := artifacts.ProjectIntent{Kind: "design", SubmissionID: "review-design-original", PageID: page, BaseVersion: 1}
	if err := f.pages.RecordArtifactProjectIntent(t.Context(), f.receipt, f.a.MessageID, intent); err != nil {
		t.Fatal("typed Design intent rejected while project feature disabled", err)
	}
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-design-review-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	host := NewInternalHandler(f.rig.db, master, newTestLogger())
	host.SetJournal(writer)
	raw, _ := json.Marshal(map[string]any{"id": f.a.RunID, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": f.a.MessageID, "artifact_project_intent": intent})
	admitted := httptest.NewRecorder()
	host.CreateRun(admitted, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
	if admitted.Code != 201 {
		t.Fatalf("typed Design run admission %d %s", admitted.Code, admitted.Body)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	tool := func(operation, slug, payload string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"operation": operation, "slug": slug, "payload": json.RawMessage(payload)})
		r := httptest.NewRequest("POST", "/api/v1/internal/pages/design-tool", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
		w := httptest.NewRecorder()
		f.pages.DesignTool(w, r.WithContext(ctx))
		return w
	}
	op := `{"operations":[{"op":"upsert","node":{"id":"cart","type":"frame","name":"Košík","x":0,"y":0,"width":390,"height":844,"fill":"#fff"}},{"op":"upsert","node":{"id":"cart-title","parentId":"cart","type":"text","name":"Title","text":"Košík","x":20,"y":20,"width":300,"height":40,"fill":"#333"}}]}`
	if w := tool("design_apply", page, op); w.Code != 200 {
		t.Fatalf("offline original-run apply %d %s", w.Code, w.Body)
	}
	// The model receives the selected immutable Page ID. A different Page may
	// legally use that text as its slug; the exact run mandate disambiguates it.
	if w := human(page, fmt.Sprintf(`{"slug":%q,"name":"ID collision"}`, page), f.pages.CreateDesign); w.Code != 201 {
		t.Fatalf("collision fixture %d %s", w.Code, w.Body)
	}
	for _, reference := range []string{page, "@" + page} {
		for _, payload := range []string{`{}`, `{"version":2}`} {
			w := tool("design_get", reference, payload)
			var selected struct {
				PageID string `json:"page_id"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &selected) != nil || selected.PageID != page {
				t.Fatalf("selected immutable reference %q resolved incorrectly: %d %s", reference, w.Code, w.Body)
			}
		}
	}
	if w := tool("design_get", "review-design-a", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"parentId":"cart"`) || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatalf("offline retained Yjs canvas %d %s", w.Code, w.Body)
	}
	var otherPage string
	if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='review-design-b'`).Scan(&otherPage); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"design_get", "design_apply", "design_snapshot", "design_versions", "design_restore"} {
		payload := `{}`
		if operation == "design_apply" {
			payload = op
		}
		if operation == "design_restore" {
			payload = `{"version":1}`
		}
		absent := tool(operation, "review-design-missing", payload)
		other := tool(operation, "review-design-b", payload)
		if absent.Code != 404 || other.Code != 404 || absent.Body.String() != other.Body.String() {
			t.Errorf("run probed another owned Design via %s: absent %d %s other %d %s", operation, absent.Code, absent.Body, other.Code, other.Body)
		}
		for _, reference := range []string{otherPage, "@" + otherPage} {
			if denied := tool(operation, reference, payload); denied.Code != 404 || denied.Body.String() != absent.Body.String() {
				t.Errorf("run probed another immutable Design via %s: %d %s", operation, denied.Code, denied.Body)
			}
		}
	}
	mustExec(t, f.rig.db, `UPDATE artifact_project_message_intents SET revoked_at='2026-10-28T00:00:00Z' WHERE run_id=?`, f.a.RunID)
	if w := tool("design_apply", "review-design-a", op); w.Code != 403 {
		t.Fatalf("revoked typed Design intent still writes %d %s", w.Code, w.Body)
	}
	var versions int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id=?`, page).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("denied agent calls changed history %d %v", versions, err)
	}
}

// The socket checks current authorization for both directions. A browser that
// keeps an old connection after a grant downgrade cannot smuggle a syncStep2.
func TestDesignWebsocketReaderAndDowngradedEditorCannotWrite(t *testing.T) {
	h, ws, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Reviewed socket"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "share", `{"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	user := "design-socket-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'design-socket-reader@example.test','Socket Reader')`, user)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, user)
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='board'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, user, owner)
	token := mintTokenFor(t, h.db, user, "reviewed-design-socket")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), ws, "MEMBER")
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "board")
		h.DesignWebsocket(w, r)
	}))
	defer server.Close()
	dial := func() *websocket.Conn {
		t.Helper()
		header := http.Header{"Authorization": []string{"Bearer " + token}}
		conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/pages/board/design/ws", header)
		if err != nil {
			if response != nil {
				t.Fatalf("socket upgrade %d: %v", response.StatusCode, err)
			}
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err = conn.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err != nil {
			t.Fatal(err)
		}
		for {
			typ, frame, err := conn.ReadMessage()
			if err != nil || typ != websocket.BinaryMessage {
				t.Fatalf("reader standard initial sync: %d %v", typ, err)
			}
			kind, sub, err := designSyncKind(frame)
			if err != nil {
				t.Fatal(err)
			}
			if kind == 0 && sub == 1 {
				break
			}
		}
		return conn
	}
	update, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"unauthorized","type":"rect","x":0,"y":0,"width":10,"height":10}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 {
		t.Helper()
		var n int64
		if err := h.db.QueryRow(`SELECT revision FROM artifact_design_documents WHERE page_id=?`, page).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	assertClosed := func(conn *websocket.Conn) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		_, _, err := conn.ReadMessage()
		if !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
			t.Fatalf("authorization denial did not close with 1008: %v", err)
		}
	}
	reader := dial()
	before := revision()
	if err = reader.WriteMessage(websocket.BinaryMessage, update.Frame); err != nil {
		t.Fatal(err)
	}
	assertClosed(reader)
	if revision() != before {
		t.Fatal("Read grant wrote a Yjs syncStep2")
	}
	mustExec(t, h.db, `UPDATE page_grants SET level='write' WHERE page_id=? AND subject_id=?`, page, user)
	editor := dial()
	mustExec(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_id=?`, page, user)
	if err = editor.WriteMessage(websocket.BinaryMessage, update.Frame); err != nil {
		t.Fatal(err)
	}
	assertClosed(editor)
	if revision() != before {
		t.Fatal("downgraded editor wrote on its old socket")
	}
	reader = dial()
	mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, user)
	// Broadcasting a valid owner update must close the revoked reader before
	// that queued content can be delivered.
	body := `{"operations":[{"op":"upsert","node":{"id":"private-after-revoke","type":"text","x":0,"y":0,"width":100,"height":20,"text":"secret-after-revoke"}}]}`
	if w := call(owner, "apply", body); w.Code != 200 {
		t.Fatalf("owner update %d %s", w.Code, w.Body)
	}
	assertClosed(reader)
}
