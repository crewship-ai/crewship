package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func (s *chatRunScenario) chatRefs(chat, cursor string, limit string) (int, chatArtifactRefsPage) {
	s.t.Helper()
	var role string
	if err := s.f.rig.db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, s.f.a.WorkspaceID, s.f.a.HumanUserID).Scan(&role); err != nil {
		s.t.Fatal(err)
	}
	router := &Router{db: s.f.rig.db, logger: newTestLogger()}
	url := "/api/v1/chats/" + chat + "/artifact-refs?limit=" + limit
	if cursor != "" {
		url += "&cursor=" + cursor
	}
	req := httptest.NewRequest(http.MethodGet, url, nil).WithContext(context.WithValue(s.human, ctxRole, role))
	req.SetPathValue("chatId", chat)
	w := httptest.NewRecorder()
	router.chatArtifactRefs(s.m.human)(w, req)
	var out chatArtifactRefsPage
	if w.Code == 200 && json.Unmarshal(w.Body.Bytes(), &out) != nil {
		s.t.Fatalf("body %s", w.Body)
	}
	return w.Code, out
}

func (s *chatRunScenario) attachOnly(pages ...string) {
	s.t.Helper()
	if err := s.trySend("Look at this", pages...); !errors.Is(err, errScenarioModel) {
		s.t.Fatalf("the message did not reach a run: %v", err)
	}
}

func refPages(p chatArtifactRefsPage) []string {
	out := []string{}
	for _, r := range p.Refs {
		out = append(out, r.PageID)
	}
	return out
}

func TestChatArtifactRefsListWhatThePersonAttachedAndMaySee(t *testing.T) {
	s := newChatRunScenario(t, true)
	s.seedSheet("sheet-second", s.f.a.HumanUserID)
	s.attachOnly("sheet-own", "sheet-second")

	code, page := s.chatRefs(s.f.a.ChatID, "", "100")
	if code != 200 || len(page.Refs) != 2 || page.Refs[0].PageID != "sheet-own" || page.Refs[0].Slug != "sheet-own" || page.Refs[0].Application != "gridcraft" || page.Refs[0].MessageID == "" || page.NextCursor != "" {
		t.Fatalf("%d %+v", code, page)
	}

	// One at a time: the cursor carries on after the first.
	code, first := s.chatRefs(s.f.a.ChatID, "", "1")
	if code != 200 || len(first.Refs) != 1 || first.NextCursor == "" {
		t.Fatalf("first page %d %+v", code, first)
	}
	code, second := s.chatRefs(s.f.a.ChatID, first.NextCursor, "1")
	if code != 200 || len(second.Refs) != 1 || second.Refs[0].PageID == first.Refs[0].PageID {
		t.Fatalf("second page %d %+v", code, second)
	}

	// A page the person may no longer see is neither listed nor counted.
	other := s.f.rig.adminID
	if other == s.f.a.HumanUserID {
		other = s.f.rig.userID
	}
	mustExec(t, s.f.rig.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, s.f.a.WorkspaceID, s.f.a.HumanUserID)
	mustExec(t, s.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='sheet-own'`, other)
	code, first = s.chatRefs(s.f.a.ChatID, "", "1")
	if code != 200 || len(first.Refs) != 1 || first.Refs[0].PageID != "sheet-second" || first.NextCursor != "" {
		t.Fatalf("hidden page shows or is counted: %d %+v", code, first)
	}

	// A deleted page is gone with its references.
	mustExec(t, s.f.rig.db, `DELETE FROM pages WHERE id='sheet-second'`)
	if code, page = s.chatRefs(s.f.a.ChatID, "", "100"); code != 200 || len(page.Refs) != 0 {
		t.Fatalf("deleted page: %d %v", code, refPages(page))
	}
}

func TestChatArtifactRefsSomeoneElsesChatIsMissing(t *testing.T) {
	s := newChatRunScenario(t, true)
	s.attachOnly("sheet-own")
	if code, _ := s.chatRefs("chat-that-does-not-exist", "", "100"); code != 404 {
		t.Fatalf("missing chat: %d", code)
	}
	// The same chat started by someone else, for a MEMBER (an OWNER or ADMIN
	// reads every private chat by the chat rules, and sees what they show).
	other := s.f.rig.adminID
	if other == s.f.a.HumanUserID {
		other = s.f.rig.userID
	}
	mustExec(t, s.f.rig.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, s.f.a.WorkspaceID, s.f.a.HumanUserID)
	mustExec(t, s.f.rig.db, `UPDATE chats SET created_by=? WHERE id=?`, other, s.f.a.ChatID)
	if code, _ := s.chatRefs(s.f.a.ChatID, "", "100"); code != 404 {
		t.Fatalf("someone else's chat: %d", code)
	}
	for _, bad := range []string{"0", "101", "x"} {
		if code, _ := s.chatRefs(s.f.a.ChatID, "", bad); code != 400 && code != 404 {
			t.Fatalf("limit %s: %d", bad, code)
		}
	}
}

// A Sheet opened from a file in the chat is listed at once, as an upload; once a message attaches
// it, it is listed once, as that message's.
func TestChatArtifactRefsListASheetOpenedFromAFileInTheChat(t *testing.T) {
	s := newChatRunScenario(t, true)
	s.seedSheet("sheet-upload", s.f.a.HumanUserID)
	mustExec(t, s.f.rig.db, `INSERT INTO artifact_chat_refs(chat_id,page_id,workspace_id,human_user_id,created_at) VALUES(?,?,?,?,?)`,
		s.f.a.ChatID, "sheet-upload", s.f.a.WorkspaceID, s.f.a.HumanUserID, "2026-11-26T10:00:00.000Z")
	code, page := s.chatRefs(s.f.a.ChatID, "", "100")
	if code != 200 || len(page.Refs) != 1 || page.Refs[0].PageID != "sheet-upload" || page.Refs[0].Source != "upload" || page.Refs[0].MessageID != "" || page.Refs[0].Slug != "sheet-upload" {
		t.Fatalf("upload %d %+v", code, page)
	}
	s.attachOnly("sheet-upload")
	code, page = s.chatRefs(s.f.a.ChatID, "", "100")
	if code != 200 || len(page.Refs) != 1 || page.Refs[0].Source != "message" || page.Refs[0].MessageID == "" {
		t.Fatalf("attached %d %+v", code, page)
	}
	// Paging over an upload row carries on after it.
	s.seedSheet("sheet-later", s.f.a.HumanUserID)
	mustExec(t, s.f.rig.db, `INSERT INTO artifact_chat_refs(chat_id,page_id,workspace_id,human_user_id,created_at) VALUES(?,?,?,?,?)`,
		s.f.a.ChatID, "sheet-later", s.f.a.WorkspaceID, s.f.a.HumanUserID, "2026-11-26T09:00:00.000Z")
	code, first := s.chatRefs(s.f.a.ChatID, "", "1")
	if code != 200 || len(first.Refs) != 1 || first.Refs[0].PageID != "sheet-later" || first.NextCursor == "" {
		t.Fatalf("first %d %+v", code, first)
	}
	code, second := s.chatRefs(s.f.a.ChatID, first.NextCursor, "1")
	if code != 200 || len(second.Refs) != 1 || second.Refs[0].PageID != "sheet-upload" {
		t.Fatalf("second %d %+v", code, second)
	}
}
