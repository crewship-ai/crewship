package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/groupchat"
)

func TestWorkspaceConversationsHTTP(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	for _, u := range []string{"colleague", "outsider"} {
		if _, err := db.Exec(`INSERT INTO users(id,email,full_name)VALUES(?,?,?)`, u, u+"@test.invalid", u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role)VALUES('colleague-member',?,'colleague','MEMBER')`, ws); err != nil {
		t.Fatal(err)
	}
	h := NewWorkspaceConversationsHandler(groupchat.New(db), newTestLogger())
	request := func(fn http.HandlerFunc, method, user, conv, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/conversations", strings.NewReader(body))
		r.SetPathValue("conversationId", conv)
		r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), ws, "MEMBER"))
		w := httptest.NewRecorder()
		fn(w, r)
		return w
	}
	c := request(h.Create, "POST", owner, "", `{"title":"Planning","kind":"group","member_ids":["colleague"]}`)
	if c.Code != 201 {
		t.Fatalf("create: %d %s", c.Code, c.Body.String())
	}
	var conv groupchat.Conversation
	if err := json.Unmarshal(c.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []http.HandlerFunc{h.Get, h.Messages, h.Members} {
		w := request(fn, "GET", "outsider", conv.ID, "")
		if w.Code != 404 {
			t.Fatalf("outsider: %d %s", w.Code, w.Body.String())
		}
	}
	send := request(h.Send, "POST", "colleague", conv.ID, `{"client_id":"retry-one","content":"hello"}`)
	if send.Code != 201 {
		t.Fatalf("send: %d %s", send.Code, send.Body.String())
	}
	retry := request(h.Send, "POST", "colleague", conv.ID, `{"client_id":"retry-one","content":"hello"}`)
	if retry.Code != 200 || retry.Body.String() != send.Body.String() {
		t.Fatalf("retry differs: %d %s", retry.Code, retry.Body.String())
	}
	conflict := request(h.Send, "POST", "colleague", conv.ID, `{"client_id":"retry-one","content":"different"}`)
	if conflict.Code != 409 {
		t.Fatalf("conflict: %d", conflict.Code)
	}
	spoof := request(h.Send, "POST", "colleague", conv.ID, `{"client_id":"spoof","content":"hello","author_user_id":"`+owner+`"}`)
	if spoof.Code != 400 {
		t.Fatalf("spoof accepted: %d", spoof.Code)
	}
	sourceSpoof := request(h.Send, "POST", "colleague", conv.ID, `{"client_id":"activity:5","content":"spoof","source_kind":"activity"}`)
	if sourceSpoof.Code != 400 {
		t.Fatalf("trusted source spoof accepted: %d", sourceSpoof.Code)
	}
	if w := request(h.Read, "POST", owner, conv.ID, `{"last_read_sequence":1}`); w.Code != 204 {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	if err := h.store.RemoveMember(t.Context(), ws, owner, conv.ID, "colleague"); err != nil {
		t.Fatal(err)
	}
	if w := request(h.Messages, "GET", "colleague", conv.ID, ""); w.Code != 404 {
		t.Fatalf("removed member still reads: %d", w.Code)
	}
	if w := request(h.Send, "POST", "colleague", conv.ID, `{"client_id":"removed","content":"no"}`); w.Code != 404 {
		t.Fatalf("removed member still writes: %d", w.Code)
	}
}

func TestWorkspaceConversationPaginationRejectsInvalid(t *testing.T) {
	for _, q := range []string{"?after_sequence=-1", "?after_sequence=nope", "?limit=0", "?limit=101"} {
		if _, _, err := conversationPage(httptest.NewRequest("GET", "/"+q, nil)); err == nil {
			t.Errorf("accepted %s", q)
		}
	}
}

func TestWorkspaceDirectConversationHTTP(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	for _, user := range []string{"direct-peer", "direct-outsider"} {
		if _, err := db.Exec(`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, user, user+"@example.invalid", user); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('direct-peer-membership',?,'direct-peer','MEMBER')`, workspace); err != nil {
		t.Fatal(err)
	}
	handler := NewWorkspaceConversationsHandler(groupchat.New(db), newTestLogger())
	request := func(user, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/conversations/direct", strings.NewReader(body))
		if user != "" {
			r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), workspace, "OWNER"))
		}
		w := httptest.NewRecorder()
		handler.Direct(w, r)
		return w
	}
	if w := request("", `{"user_id":"direct-peer"}`); w.Code != 401 {
		t.Fatalf("unauthenticated=%d", w.Code)
	}
	first := request(owner, `{"user_id":"direct-peer"}`)
	if first.Code != 201 {
		t.Fatalf("create=%d %s", first.Code, first.Body.String())
	}
	var direct groupchat.Conversation
	if err := json.Unmarshal(first.Body.Bytes(), &direct); err != nil {
		t.Fatal(err)
	}
	if !direct.IsDirect || direct.Kind != "group" {
		t.Fatalf("wrong direct %+v", direct)
	}
	second := request("direct-peer", `{"user_id":"`+owner+`"}`)
	if second.Code != 200 {
		t.Fatalf("reopen=%d %s", second.Code, second.Body.String())
	}
	var existing groupchat.Conversation
	if err := json.Unmarshal(second.Body.Bytes(), &existing); err != nil {
		t.Fatal(err)
	}
	if existing.ID != direct.ID || !existing.IsDirect {
		t.Fatalf("reopen changed identity %+v", existing)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{{`{"user_id":"` + owner + `"}`, 400}, {`{"user_id":"direct-outsider"}`, 404}, {`{"user_id":"direct-peer","created_by":"direct-outsider"}`, 400}, {`{"user_id":"direct-peer","is_direct":false}`, 400}, {`{"user_id":"direct-peer","member_ids":["direct-outsider"]}`, 400}} {
		if w := request(owner, tc.body); w.Code != tc.status {
			t.Fatalf("body%s status%d want%d", tc.body, w.Code, tc.status)
		}
	}
	outsiderRequest := httptest.NewRequest("GET", "/api/v1/conversations/"+direct.ID, nil)
	outsiderRequest.SetPathValue("conversationId", direct.ID)
	outsiderRequest = outsiderRequest.WithContext(withWorkspace(withUser(outsiderRequest.Context(), &AuthUser{ID: "direct-outsider"}), workspace, "OWNER"))
	w := httptest.NewRecorder()
	handler.Get(w, outsiderRequest)
	if w.Code != 404 {
		t.Fatalf("owner role leaked private direct=%d", w.Code)
	}
	ordinary, err := handler.store.Create(t.Context(), workspace, owner, groupchat.CreateInput{Title: "Normal group", Kind: "group"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	flag, present := wire["is_direct"]
	if !present || flag != false {
		t.Fatalf("ordinary conversation must wire is_direct false: %s", data)
	}
}
