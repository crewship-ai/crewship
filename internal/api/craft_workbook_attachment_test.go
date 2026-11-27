package api

import (
	"strings"
	"testing"
)

const attachedWorkbookMessage = "I've attached a file:\n- attachments/ab12/Loan_plan.xlsx\nClear the Loan ID column"

// A message that carries only the uploaded xlsx, with no Sheet reference, binds no Sheet even when a
// Sheet of this chat has the file's name: the message reaches the agent, and the
// Sheet tools refuse with the text that tells it what to do.
func TestAttachedWorkbookAloneBindsNoSheet(t *testing.T) {
	s := newSheetITScenario(t)
	mustExec(t, s.f.rig.db, `UPDATE pages SET name='Loan_plan' WHERE id='sheet-own'`)
	mustExec(t, s.f.rig.db, `INSERT OR IGNORE INTO artifact_chat_refs(chat_id,page_id,workspace_id,human_user_id,created_at) VALUES(?,?,?,?,'2026-11-26T00:00:00Z')`, s.f.a.ChatID, "sheet-own", s.f.a.WorkspaceID, s.f.a.HumanUserID)
	s.send(attachedWorkbookMessage, false)
	call := s.agent()
	if text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}}); !refused || strings.Contains(text, `"name":"L"`) {
		t.Fatalf("the attachment alone bound the Sheet: %s", text)
	}
}

// A workbook attachment that is no Sheet of this chat binds nothing; the refusal tells the agent why
// and what to do instead of a bare 403, and never to work on a copy of the file.
func TestUnboundSheetRefusalTellsTheAgentWhatToDo(t *testing.T) {
	s := newSheetITScenario(t)
	mustExec(t, s.f.rig.db, `UPDATE pages SET name='Something else' WHERE id='sheet-own'`)
	s.send(attachedWorkbookMessage, false)
	call := s.agent()
	text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}})
	if !refused || !strings.Contains(text, "agent edits") || !strings.Contains(text, "copy") {
		t.Fatalf("refusal: %v %s", refused, text)
	}
}
