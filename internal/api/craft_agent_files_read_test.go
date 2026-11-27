package api

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// The agent knows a Sheet it was given (agent_files) in every private chat of the same person with
// it: without the chip it may read it when the person still may and the chat is that person's own
// private chat with this agent, and each such read is audited as source agent_files. Writing still
// needs the chip in the message.
func TestAgentFileIsReadableWithoutTheChipButNotWritable(t *testing.T) {
	s := newSheetITScenario(t)
	mustExec(t, s.f.rig.db, `INSERT OR IGNORE INTO agent_files(agent_id,page_id,workspace_id,added_by,added_at) VALUES(?,?,?,?,'2026-11-26T00:00:00Z')`, s.f.a.AgentID, "sheet-own", s.f.a.WorkspaceID, s.f.a.HumanUserID)
	s.send("What is in my strategy Sheet?", false)
	call := s.agent()
	if text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}}); refused || !strings.Contains(text, `"name":"L"`) {
		t.Fatalf("the agent's own file is not readable: %s", text)
	}
	s.requireAgentFileReadAudit()
	if text, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{}); !refused || !strings.Contains(text, "agent edits") {
		t.Fatalf("the agent took the lease without the chip: %v %s", refused, text)
	}
	if s.head() != 1 {
		t.Fatalf("the Sheet moved to %d", s.head())
	}
}

func (s *sitScenario) requireAgentFileReadAudit() {
	s.t.Helper()
	var source string
	if err := s.f.rig.db.QueryRow(`SELECT json_extract(payload,'$.source') FROM journal_entries WHERE workspace_id=? AND entry_type=? AND json_extract(payload,'$.page_id')='sheet-own' AND entry_hash <> '' ORDER BY seq DESC LIMIT 1`, s.f.a.WorkspaceID, journal.EntryArtifactSheetRead).Scan(&source); err != nil || source != "agent_files" {
		s.t.Fatalf("the read is not audited as agent_files: %q %v", source, err)
	}
}

// agent_files alone gives nothing: taken back, in a chat that is not the person's private chat with
// the agent, or once the person lost the right to the Sheet.
func TestAgentFileAloneGrantsNothingOutsideItsThreeConditions(t *testing.T) {
	for name, revoke := range map[string]string{
		"the file was taken back":       `UPDATE agent_files SET removed_at='2026-11-26T00:00:00Z', removed_by=added_by`,
		"the chat is shared":            `UPDATE chats SET visibility='group'`,
		"the person left the workspace": `DELETE FROM workspace_members WHERE user_id=(SELECT human_user_id FROM artifact_run_principals LIMIT 1)`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newSheetITScenario(t)
			mustExec(t, s.f.rig.db, `INSERT OR IGNORE INTO agent_files(agent_id,page_id,workspace_id,added_by,added_at) VALUES(?,?,?,?,'2026-11-26T00:00:00Z')`, s.f.a.AgentID, "sheet-own", s.f.a.WorkspaceID, s.f.a.HumanUserID)
			s.send("What is in my strategy Sheet?", false)
			call := s.agent()
			mustExec(t, s.f.rig.db, revoke)
			if text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}}); !refused {
				t.Fatalf("read through agent_files alone: %s", text)
			}
		})
	}
}
