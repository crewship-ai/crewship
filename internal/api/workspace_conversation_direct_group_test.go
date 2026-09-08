package api

import (
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/groupchat"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceConversationContinueHTTP(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	for _, user := range []string{"continue-peer", "continue-new", "continue-other"} {
		if _, err := db.Exec(`INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, "wm-"+user, ws, user); err != nil {
			t.Fatal(err)
		}
	}
	store := groupchat.New(db)
	dm, _, err := store.OpenDirect(t.Context(), ws, owner, "continue-peer")
	if err != nil {
		t.Fatal(err)
	}
	h := NewWorkspaceConversationsHandler(store, newTestLogger())
	body := `{"kind":"group","title":"Planning","member_ids":["continue-new"],"client_id":"retry-one"}`
	call := func(user, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.SetPathValue("conversationId", dm.ID)
		r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), ws, "MEMBER"))
		w := httptest.NewRecorder()
		h.Continue(w, r)
		return w
	}
	first := call("continue-peer", body)
	if first.Code != 201 {
		t.Fatalf("create %d %s", first.Code, first.Body.String())
	}
	retry := call("continue-peer", body)
	if retry.Code != 200 {
		t.Fatalf("retry %d %s", retry.Code, retry.Body.String())
	}
	var created, retried groupchat.Conversation
	_ = json.Unmarshal(first.Body.Bytes(), &created)
	_ = json.Unmarshal(retry.Body.Bytes(), &retried)
	if created.ID == "" || created.ID != retried.ID || retried.LastSequence != 0 {
		t.Fatal("retry changed room/history")
	}
	if w := call("continue-peer", strings.Replace(body, "Planning", "Changed", 1)); w.Code != 409 {
		t.Fatalf("conflict %d", w.Code)
	}
	if w := call("continue-other", body); w.Code != 404 {
		t.Fatalf("outsider %d", w.Code)
	}
	if w := call("continue-peer", `{"kind":"group","title":"Planning","member_ids":["continue-new"],"client_id":"retry-two","copy_history":true}`); w.Code != 400 {
		t.Fatalf("unsupported history copy %d", w.Code)
	}
}
