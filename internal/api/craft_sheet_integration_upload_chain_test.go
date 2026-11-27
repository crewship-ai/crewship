package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// sitUpload is the person's xlsx upload in a chat: craft/initialize with X-Craft-Chat.
func (s *sitScenario) sitUpload(page, chat string, body []byte) *httptest.ResponseRecorder {
	s.t.Helper()
	mustExec(s.t, s.f.rig.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,?,?,?,'{}',1)`, page, s.f.a.WorkspaceID, page, page, s.f.a.HumanUserID)
	r := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/initialize", bytes.NewReader(body)).WithContext(s.human)
	r.SetPathValue("slug", "@"+page)
	r.Header.Set("Content-Type", mooring.WorkbookMediaTypeXLSM)
	r.Header.Set("X-Craft-File-Name", "Plan.xlsm")
	r.Header.Set("X-Craft-Chat", chat)
	w := httptest.NewRecorder()
	s.m.documents.initialize(w, r)
	return w
}

// sitRunEnds stands for the agent's run finishing: one live chat run per agent (SerialAgentLimits),
// so the next message starts its own run.
func (s *sitScenario) sitRunEnds() {
	s.t.Helper()
	mustExec(s.t, s.f.rig.db, `UPDATE work_items SET state='succeeded' WHERE agent_id=? AND state IN ('queued','starting','running')`, s.f.a.AgentID)
	mustExec(s.t, s.f.rig.db, `UPDATE work_attempts SET ended_at='2026-11-26T00:00:00Z' WHERE ended_at IS NULL`)
}

func (s *sitScenario) sitReadsTheUpload(call func(string, any) (string, bool, json.RawMessage)) (string, bool) {
	text, refused := s.read(call, map[string]any{"command": "cell.get", "params": map[string]any{"sheet": "L", "cell": "B1"}})
	return text, !refused && strings.Contains(text, `"value":"Status"`)
}

// The chain of an uploaded workbook. The person uploads an xlsx in the agent's chat (initialize
// with X-Craft-Chat): the chat holds it (artifact_chat_refs) and the agent keeps it (agent_files).
//   - A later message with the Sheet chip and no file attachment line: the agent reads its data.
//   - A message without any chip, in the same private chat: the agent reads it (agent_files, the agent-files rule).
//   - A new private chat of the same agent, no chip: the agent reads it through agent_files; a
//     change still needs the chip (403 not_attached).
func TestSheetITUploadedWorkbookStaysWithTheChatAndTheAgent(t *testing.T) {
	s := newSheetITScenario(t)
	db := s.f.rig.db
	if w := s.sitUpload("sheet-up", s.f.a.ChatID, sitWorkbook(sitInitialL(), sitInitialOther())); w.Code < 200 || w.Code > 299 {
		t.Fatalf("upload (initialize with X-Craft-Chat): %d %s", w.Code, w.Body)
	}
	var refs, files int
	_ = db.QueryRow(`SELECT count(*) FROM artifact_chat_refs WHERE chat_id=? AND page_id='sheet-up'`, s.f.a.ChatID).Scan(&refs)
	_ = db.QueryRow(`SELECT count(*) FROM agent_files WHERE agent_id=? AND page_id='sheet-up' AND removed_at IS NULL`, s.f.a.AgentID).Scan(&files)
	if refs != 1 || files != 1 {
		t.Fatalf("after the upload: chat refs %d, agent files %d; want 1 and 1", refs, files)
	}

	// The Sheet chip, no file attachment line.
	if err := s.trySend("What is the status of item 7?", "sheet-up"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("the message did not reach a run: %v", err)
	}
	call := s.agent()
	if text, ok := s.sitReadsTheUpload(call); !ok {
		t.Fatalf("with the Sheet chip the agent cannot read the uploaded workbook: %.300s", text)
	}

	// No chip at all, same chat.
	s.sitRunEnds()
	if err := s.trySend("And item 8?"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("the second message did not reach a run: %v", err)
	}
	if text, ok := s.sitReadsTheUpload(s.agent()); !ok {
		t.Errorf("same private chat, no chip: the agent cannot read its file (agent_files, the agent-files rule): %.200s", text)
	}

	// A new chat of the same agent, no chip.
	mustExec(t, db, `INSERT INTO chats(id,agent_id,workspace_id,created_by,visibility) VALUES('sit-chat-next',?,?,?,'private')`, s.f.a.AgentID, s.f.a.WorkspaceID, s.f.a.HumanUserID)
	s.sitRunEnds()
	first := s.f.a.ChatID
	s.f.a.ChatID = "sit-chat-next"
	t.Cleanup(func() { s.f.a.ChatID = first })
	if err := s.trySend("Open Plan.xlsm again and tell me the status of item 7"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("the message in the new chat did not reach a run: %v", err)
	}
	next := s.agent()
	if text, ok := s.sitReadsTheUpload(next); !ok {
		t.Errorf("a new private chat of the same agent, no chip: the uploaded workbook is not read through agent_files (the agent-files rule): %.200s", text)
	}
	apply, refused := s.toolCall(next, "craft_sheet_apply", map[string]any{"generation": "1", "base_version": "1", "request_id": "sit-up-next", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "x"}))})
	if !refused || !strings.Contains(apply, "not_attached") || !strings.Contains(strings.ToLower(apply), "attach") {
		t.Errorf("a change in the new chat without a chip must be 403 not_attached, told plainly: refused=%v %.200s", refused, apply)
	}
	var versions int
	_ = db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='sheet-up'`).Scan(&versions)
	if versions != 1 {
		t.Errorf("the uploaded workbook has %d versions, want 1", versions)
	}
}

// sitNewPage is a page as POST /api/v1/pages makes it before the upload: the person's, no folder, no
// crew, private_audience 0, just made.
func (s *sitScenario) sitNewPage(id, owner string) {
	s.t.Helper()
	mustExec(s.t, s.f.rig.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, id, s.f.a.WorkspaceID, id, "Plan", owner)
}

func (s *sitScenario) sitInitializeChat(page, chat string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.sitInitializeChatFrom(page, chat, sitAttachedPlanPath)
}

// sitInitializeChatFrom is the upload of a file into page, from chat (path names the attachment the
// file came from; the server is told nothing of it: a Sheet is bound only by artifact_refs).
func (s *sitScenario) sitInitializeChatFrom(page, chat, _ string) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/initialize", bytes.NewReader(sitWorkbook(sitInitialL(), sitInitialOther()))).WithContext(s.human)
	r.SetPathValue("slug", "@"+page)
	r.Header.Set("Content-Type", mooring.WorkbookMediaTypeXLSM)
	r.Header.Set("X-Craft-File-Name", "Plan.xlsx")
	r.Header.Set("X-Craft-Chat", chat)
	w := httptest.NewRecorder()
	s.m.documents.initialize(w, r)
	return w
}

func (s *sitScenario) sitAudience(page string) int {
	var v int
	_ = s.f.rig.db.QueryRow(`SELECT private_audience FROM pages WHERE id=?`, page).Scan(&v)
	return v
}

func (s *sitScenario) sitAgentAccess(page string) craftNativeAgentAccessResponse {
	s.t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/pages/@"+page+"/craft/agent-access?chat_id="+s.f.a.ChatID, nil).WithContext(s.human)
	r.SetPathValue("slug", "@"+page)
	w := httptest.NewRecorder()
	s.m.human.agentAccess(w, r)
	var out craftNativeAgentAccessResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 {
		out.Reason = fmt.Sprintf("http %d %s", w.Code, w.Body)
	}
	return out
}

// The browser path "new chat, xlsx, send right away", server side: the person's page made by POST /pages
// (private_audience 0) is initialized from the file in their private chat with the agent. It is
// then private to them, the agent-access check allows it, and the message with its chip
// (artifact_refs, the composer's choice) binds it: the agent reads and changes it. A group chat, another person's page and a page
// that already has a version keep their audience.
func TestSheetITSheetFromAFileInAPrivateChatIsPrivateAndBound(t *testing.T) {
	t.Run("private chat", func(t *testing.T) {
		s := newSheetITScenario(t)
		s.sitNewPage("sheet-new", s.f.a.HumanUserID)
		if w := s.sitInitializeChat("sheet-new", s.f.a.ChatID); w.Code < 200 || w.Code > 299 {
			t.Fatalf("initialize of a new page from the person's private chat: %d %s", w.Code, w.Body)
		}
		if a := s.sitAudience("sheet-new"); a != 1 {
			t.Errorf("private_audience %d after the upload in a private chat, want 1", a)
		}
		// Right after the upload the person's own editor holds the Sheet; agent-access allows it
		// (decision (a): the lease of the same person does not close the chip).
		if access := s.sitAgentAccess("sheet-new"); !access.Allowed {
			t.Errorf("agent-access of the new private Sheet while its person's editor holds it: %+v", access)
		}
		mustExec(t, s.f.rig.db, `DELETE FROM mooring_edit_leases WHERE page_id='sheet-new'`) // the person's editor closed
		call := s.sitSendAttached(scenarioRefs("sheet-new"))
		if call == nil {
			t.Fatal("the message with the file did not reach the agent")
		}
		if text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}}); refused || !strings.Contains(text, `"name":"L"`) {
			t.Fatalf("the message with the new Sheet's chip does not bind it: %.200s", text)
		}
		generation := s.acquire(call)
		applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-new", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
		if !strings.Contains(applied, `"version":2`) || !strings.Contains(applied, `"page_id":"sheet-new"`) {
			t.Errorf("the agent's change of the new Sheet: %.200s", applied)
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(*sitScenario) (page string)
	}{
		{"group chat", func(s *sitScenario) string {
			s.sitNewPage("sheet-new", s.f.a.HumanUserID)
			mustExec(t, s.f.rig.db, `INSERT INTO users(id,email) VALUES('sit-member','sit-member@example.invalid')`)
			mustExec(t, s.f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, s.f.a.ChatID)
			mustExec(t, s.f.rig.db, `INSERT INTO chat_participants(chat_id,user_id) VALUES(?,?),(?,'sit-member')`, s.f.a.ChatID, s.f.a.HumanUserID, s.f.a.ChatID)
			return "sheet-new"
		}},
		{"page that already has a version", func(s *sitScenario) string {
			mustExec(t, s.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-own'`)
			return "sheet-own"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			page := tc.setup(s)
			w := s.sitInitializeChat(page, s.f.a.ChatID)
			if a := s.sitAudience(page); a != 0 {
				t.Errorf("%s: private_audience %d after initialize (%d), want unchanged 0", tc.name, a, w.Code)
			}
		})
	}
	// an originally shared page made more than 15 minutes ago, filled
	// from a private chat's file, either ends with a verified private first version or stops before
	// it stores any content; a page that is already private passes with nothing to change.
	t.Run("shared page made long ago, private chat", func(t *testing.T) {
		s := newSheetITScenario(t)
		s.sitNewPage("sheet-new", s.f.a.HumanUserID)
		mustExec(t, s.f.rig.db, `UPDATE pages SET created_at='2026-01-01T00:00:00Z' WHERE id='sheet-new'`)
		w := s.sitInitializeChat("sheet-new", s.f.a.ChatID)
		var versions int
		_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='sheet-new'`).Scan(&versions)
		private := s.sitAudience("sheet-new") == 1
		stopped := w.Code >= 400 && versions == 0
		if !(w.Code == 201 && private) && !stopped {
			t.Errorf("a private chat's file stored into a page that is not private: %d %.200s, versions %d, private %v", w.Code, w.Body, versions, private)
		}
		t.Logf("outcome: %d, versions %d, private %v", w.Code, versions, private)
	})
	t.Run("already private page made long ago, private chat", func(t *testing.T) {
		s := newSheetITScenario(t)
		s.sitNewPage("sheet-new", s.f.a.HumanUserID)
		mustExec(t, s.f.rig.db, `UPDATE pages SET created_at='2026-01-01T00:00:00Z',private_audience=1 WHERE id='sheet-new'`)
		if w := s.sitInitializeChat("sheet-new", s.f.a.ChatID); w.Code != 201 || s.sitAudience("sheet-new") != 1 {
			t.Errorf("an already private page is not filled from the private chat's file: %d %.200s", w.Code, w.Body)
		}
	})
}

// sitInitializeSession is the browser's session from initialize: its token and lease generation.
func sitInitializeSession(t *testing.T, w *httptest.ResponseRecorder) (token, generation string) {
	t.Helper()
	var out craftNativeInitializeResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Token == "" || out.Generation == "" {
		t.Fatalf("initialize: %d %s", w.Code, w.Body)
	}
	return out.Token, out.Generation
}

// In the browser's order: POST /pages, initialize from the file in the private chat (the
// page made private), then the editor, now Viewing, releases the lease with the session initialize
// gave it: nobody holds it any more (holder null), though making the page private moved the
// authority epoch. The message with the Sheet's chip
// then reaches the agent, which takes the lease and saves version 2.
//
// Without the release (the person's editor still holds the Sheet, as while Editing) the agent's
// change over the head is still stored, as a co-write beside the person's lease.
func TestSheetITSheetOpenedViewingDoesNotBlockTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release bool
	}{{"editor releases on Viewing", true}, {"editor keeps the lease", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			s.sitNewPage("sheet-new", s.f.a.HumanUserID)
			token, generation := sitInitializeSession(t, s.sitInitializeChat("sheet-new", s.f.a.ChatID))
			if a := s.sitAudience("sheet-new"); a != 1 {
				t.Fatalf("private_audience %d after the upload, want 1", a)
			}
			if tc.release {
				w := craftNativeLeaseCall(s.m.human, s.human, token, "sheet-new", "release", `{"generation":"`+generation+`"}`)
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"holder":null`) || strings.Contains(w.Body.String(), `"mine":true`) {
					t.Fatalf("release with initialize's session after the page was made private: %d %s", w.Code, w.Body)
				}
			}
			call := s.sitSendAttached(scenarioRefs("sheet-new"))
			if call == nil {
				t.Fatal("the message with the file did not reach the agent")
			}
			if text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}}); refused || !strings.Contains(text, `"name":"L"`) {
				t.Fatalf("the new Sheet is not bound: %.200s", text)
			}
			lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{})
			var got struct {
				Generation string `json:"generation"`
			}
			if refused || json.Unmarshal([]byte(lease), &got) != nil || got.Generation == "" {
				t.Fatalf("the agent cannot take the Sheet the person views: %.200s", lease)
			}
			applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": got.Generation, "base_version": "1", "request_id": "sit-view", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
			if !strings.Contains(applied, `"version":2`) {
				t.Fatalf("the agent's change: %.200s", applied)
			}
		})
	}
}
