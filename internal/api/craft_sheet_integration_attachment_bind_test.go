package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/ws"
)

// The client's attachment block for an uploaded workbook, no Sheet reference.
const (
	sitAttachedPlanPath = "attachments/sit/Plan.xlsx"
	sitAttachedPlan     = "I've attached a file:\n- " + sitAttachedPlanPath + "\nMark item 7 done"
)

// sitSeedWorkbookSheet is one more native workbook Sheet with its version 1.
func (s *sitScenario) sitSeedWorkbookSheet(id, name, owner string) {
	s.t.Helper()
	seedScenarioSheet(s.t, s.f, id, owner)
	mustExec(s.t, s.f.rig.db, `UPDATE pages SET name=? WHERE id=?`, name, id)
	versions := artifacts.NewVersionStore(s.f.rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	if _, err := versions.Apply(s.t.Context(), artifacts.VersionKey{WorkspaceID: s.f.a.WorkspaceID, PageID: id}, artifacts.VersionRequest{EntryPoint: "workbook.xlsm", Files: map[string]artifacts.VersionFile{"workbook.xlsm": {MediaType: mooring.WorkbookMediaTypeXLSM, Content: s.v1}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + owner, nil }); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sitScenario) sitChatRef(page string) {
	s.t.Helper()
	mustExec(s.t, s.f.rig.db, `INSERT OR IGNORE INTO artifact_chat_refs(chat_id,page_id,workspace_id,human_user_id,created_at) VALUES(?,?,?,?,'2026-11-26T00:00:00Z')`, s.f.a.ChatID, page, s.f.a.WorkspaceID, s.f.a.HumanUserID)
}

func (s *sitScenario) sitAgentFile(page string) {
	s.t.Helper()
	mustExec(s.t, s.f.rig.db, `INSERT INTO agent_files(agent_id,page_id,workspace_id,added_by,added_at) VALUES(?,?,?,?,'2026-11-26T00:00:00Z')`, s.f.a.AgentID, page, s.f.a.WorkspaceID, s.f.a.HumanUserID)
}

// sitSendAttached sends the attachment-only message with the given metadata and reports the run's
// tool call, or nil when no run started.
func (s *sitScenario) sitSendAttached(meta map[string]any) func(string, any) (string, bool, json.RawMessage) {
	s.t.Helper()
	err := s.bridge.HandleChatMessage(s.t.Context(), s.f.a.HumanUserID, s.f.a.ChatID, sitAttachedPlan, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: meta})
	if !errors.Is(err, errScenarioModel) {
		s.t.Logf("no run: %v", err)
		return nil
	}
	return s.agent()
}

func (s *sitScenario) sitMessageRefs() int {
	var n int
	_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_message_refs`).Scan(&n)
	return n
}

// No binding by attachment. A message whose attachment block names a workbook
// but carries no artifact_refs never binds a Sheet, and always reaches the agent: (a) two Sheets of
// the name, (b) another person's Sheet, (c) an explicit empty artifact_refs, (d) an archived Sheet,
// (e) a group chat, (f) the one own Sheet of the name, (g) the Sheet made from that very file (read
// without a chip stays allowed by the agent-files rule, but nothing is bound and nothing written). A
// Sheet goes only by the explicit chip: (h) of two different files of one name, the message with the
// chip of the Sheet just made from the second binds exactly that page.
func TestSheetITAttachmentBindsOnlyTheOneOwnSheet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*sitScenario)
		meta  map[string]any
		// runs: the message itself must still reach the agent (only the Sheet is not bound).
		runs bool
	}{
		{"(a) two own Sheets of the name", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `UPDATE pages SET name='Plan' WHERE id='sheet-own'`)
			s.sitSeedWorkbookSheet("sheet-plan-2", "Plan.xlsx", s.f.a.HumanUserID)
			s.sitChatRef("sheet-own")
			s.sitChatRef("sheet-plan-2")
		}, nil, true},
		{"(b) another person's Sheet of the name", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `INSERT INTO users(id,email) VALUES('sit-stranger','sit-stranger@example.invalid')`)
			mustExec(t, s.f.rig.db, `UPDATE pages SET name='Plan',owner_user_id='sit-stranger' WHERE id='sheet-own'`)
			s.sitChatRef("sheet-own")
			s.sitAgentFile("sheet-own")
		}, nil, true},
		{"(c) explicit empty artifact_refs", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `UPDATE pages SET name='Plan' WHERE id='sheet-own'`)
			s.sitChatRef("sheet-own")
		}, map[string]any{chatbridge.ArtifactRefsMetadataKey: []any{}}, true},
		{"(d) archived", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `UPDATE pages SET name='Plan',archived_at='2026-11-26T00:00:00Z' WHERE id='sheet-own'`)
			s.sitChatRef("sheet-own")
			s.sitAgentFile("sheet-own")
		}, nil, true},
		{"(e) group chat, the Sheet given to the agent", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `UPDATE pages SET name='Plan' WHERE id='sheet-own'`)
			s.sitAgentFile("sheet-own")
			mustExec(t, s.f.rig.db, `INSERT INTO users(id,email) VALUES('sit-member','sit-member@example.invalid')`)
			mustExec(t, s.f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, s.f.a.ChatID)
			mustExec(t, s.f.rig.db, `INSERT INTO chat_participants(chat_id,user_id) VALUES(?,?),(?,'sit-member')`, s.f.a.ChatID, s.f.a.HumanUserID, s.f.a.ChatID)
		}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			tc.setup(s)
			call := s.sitSendAttached(tc.meta)
			if call == nil && tc.runs {
				t.Errorf("%s: the person's message is refused as a whole (an attached artifact is not available): a binding by attachment must never refuse the message; it must reach the agent without a Sheet", tc.name)
			}
			if call != nil {
				text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}})
				if !refused || strings.Contains(text, `"name":"L"`) {
					t.Errorf("%s: the attachment bound a Sheet: %.200s", tc.name, text)
				}
				applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": "1", "base_version": "1", "request_id": "sit-bind", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
				if !refused {
					t.Errorf("%s: the agent changed a Sheet: %.200s", tc.name, applied)
				}
			}
			if n := s.sitMessageRefs(); n != 0 || s.head() != 1 {
				t.Errorf("%s: %d message refs, head %d; want none and 1", tc.name, n, s.head())
			}
		})
	}
	// (f), (g): nothing is bound (no message ref; acquire and apply not_attached). In (g) the Sheet
	// is in the agent's files from initialize, so reading it without a chip is allowed (the agent-files rule:
	// agent_files + the person's right + their private chat with this agent, audited); in (f) it is
	// not, and the read is refused too.
	for _, tc := range []struct {
		name     string
		setup    func(*sitScenario)
		readable bool
	}{
		{"(f) the one own Sheet of the name", func(s *sitScenario) {
			mustExec(t, s.f.rig.db, `UPDATE pages SET name='Plan' WHERE id='sheet-own'`)
			s.sitChatRef("sheet-own")
		}, false},
		{"(g) the Sheet made from that very file", func(s *sitScenario) {
			s.sitNewPage("sheet-new", s.f.a.HumanUserID)
			if w := s.sitInitializeChat("sheet-new", s.f.a.ChatID); w.Code != 201 {
				t.Fatalf("initialize: %d %s", w.Code, w.Body)
			}
			mustExec(t, s.f.rig.db, `DELETE FROM mooring_edit_leases WHERE page_id='sheet-new'`)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSheetITScenario(t)
			tc.setup(s)
			call := s.sitSendAttached(nil)
			if call == nil {
				t.Fatalf("%s: the message without a chip did not reach the agent", tc.name)
			}
			text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}})
			if read := !refused && strings.Contains(text, `"name":"L"`); read != tc.readable {
				t.Errorf("%s: read without a chip %v, want %v: %.200s", tc.name, read, tc.readable, text)
			}
			if lease, refused := s.toolCall(call, "craft_sheet_acquire", map[string]any{}); !refused || !strings.Contains(lease, "not_attached") {
				t.Errorf("%s: acquire without a chip: refused=%v %.200s", tc.name, refused, lease)
			}
			applied, refused := s.toolCall(call, "craft_sheet_apply", map[string]any{"generation": "1", "base_version": "1", "request_id": "sit-unbound", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
			if !refused || !strings.Contains(applied, "not_attached") {
				t.Errorf("%s: apply without a chip: refused=%v %.200s", tc.name, refused, applied)
			}
			if s.sitMessageRefs() != 0 {
				t.Errorf("%s: %d message refs, want none", tc.name, s.sitMessageRefs())
			}
			var versions int
			_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='sheet-new'`).Scan(&versions)
			if s.head() != 1 || versions > 1 {
				t.Errorf("%s: something stored: head %d, sheet-new versions %d", tc.name, s.head(), versions)
			}
		})
	}
	// two different workbooks of the same name, each made into its
	// Sheet; the message with the chip of the second binds exactly the Sheet made from it.
	t.Run("(h) two different files of the same name", func(t *testing.T) {
		s := newSheetITScenario(t)
		second := "attachments/second/Plan.xlsx"
		for page, path := range map[string]string{"sheet-first": sitAttachedPlanPath, "sheet-second": second} {
			s.sitNewPage(page, s.f.a.HumanUserID)
			if w := s.sitInitializeChatFrom(page, s.f.a.ChatID, path); w.Code != 201 {
				t.Fatalf("initialize %s from %s: %d %s", page, path, w.Code, w.Body)
			}
		}
		mustExec(t, s.f.rig.db, `DELETE FROM mooring_edit_leases WHERE page_id IN ('sheet-first','sheet-second')`)
		message := strings.Replace(sitAttachedPlan, sitAttachedPlanPath, second, 1)
		if err := s.bridge.HandleChatMessage(s.t.Context(), s.f.a.HumanUserID, s.f.a.ChatID, message, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: scenarioRefs("sheet-second")}); !errors.Is(err, errScenarioModel) {
			t.Fatalf("the message with the second Sheet's chip did not reach the agent: %v", err)
		}
		call := s.agent()
		if text, refused := s.read(call, map[string]any{"command": "document.inspect", "params": map[string]any{}}); refused {
			t.Fatalf("the Sheet of the chip is not bound: %.200s", text)
		}
		generation := s.acquire(call)
		applied := s.tool(call, "craft_sheet_apply", map[string]any{"generation": generation, "base_version": "1", "request_id": "sit-second", "commands": sitCommands(sitCmd("cell.set", map[string]any{"sheet": "L", "cell": "B8", "input": "done"}))})
		if !strings.Contains(applied, `"page_id":"sheet-second"`) {
			t.Errorf("the agent changed another Sheet than the one of the chip: %.200s", applied)
		}
		var first int
		_ = s.f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='sheet-first'`).Scan(&first)
		if first != 1 {
			t.Errorf("the first Sheet of the name has %d versions, want 1", first)
		}
	})
}
