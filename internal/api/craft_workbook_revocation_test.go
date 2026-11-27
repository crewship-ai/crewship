package api

import (
	"testing"
)

// An agent write of a workbook Sheet without the lease, or after a right it stands on was taken
// away, is refused: the Sheet keeps version 1 and no operation is recorded. A person's save after
// they lost the workspace is refused the same way.
func TestWorkbookWritesAreRefusedWithoutLeaseOrAfterRevocation(t *testing.T) {
	apply := func(generation string) map[string]any {
		return map[string]any{"generation": generation, "base_version": "1", "request_id": "revoked", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))}
	}
	unchanged := func(t *testing.T, s *sitScenario) {
		t.Helper()
		if s.head() != 1 || len(s.opsRows()) != 0 {
			t.Fatalf("the Sheet moved: head %d, ops %+v", s.head(), s.opsRows())
		}
	}
	t.Run("an agent without the lease", func(t *testing.T) {
		s := newSheetITScenario(t)
		s.send("Mark item 7 done", true)
		call := s.agent()
		if text, refused := s.toolCall(call, "craft_sheet_apply", apply("1")); !refused {
			t.Fatalf("the agent wrote without the lease: %s", text)
		}
		unchanged(t, s)
	})
	for name, revoke := range map[string]string{
		"the person leaves the workspace": `DELETE FROM workspace_members WHERE user_id=(SELECT human_user_id FROM artifact_run_principals LIMIT 1)`,
		"the person's session ends":       `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE user_id=(SELECT human_user_id FROM artifact_run_principals LIMIT 1)`,
		"the attachment is taken back":    `DELETE FROM artifact_message_refs`,
		"the Sheet is archived":           `UPDATE pages SET archived_at='2026-01-01T00:00:00Z' WHERE id='sheet-own'`,
	} {
		t.Run("an agent after "+name, func(t *testing.T) {
			s := newSheetITScenario(t)
			s.send("Mark item 7 done", true)
			call := s.agent()
			generation := s.acquire(call)
			mustExec(t, s.f.rig.db, revoke)
			if text, refused := s.toolCall(call, "craft_sheet_apply", apply(generation)); !refused {
				t.Fatalf("the agent still wrote: %s", text)
			}
			unchanged(t, s)
		})
	}
	t.Run("a person after leaving the workspace", func(t *testing.T) {
		s := newSheetITScenario(t)
		token := s.humanSession()
		generation := s.humanLease(token)
		mustExec(t, s.f.rig.db, `DELETE FROM workspace_members WHERE user_id=?`, s.f.a.HumanUserID)
		body := sitWorkbook(sitCells{"A1": "person"}, sitInitialOther())
		if w := s.humanSave(token, generation, "1", body, "revoked-person", ""); w.Code == 200 {
			t.Fatalf("the person still saved: %s", w.Body)
		}
		unchanged(t, s)
	})
}
