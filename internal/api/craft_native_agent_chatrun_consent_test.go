package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/ws"
)

// sendSeen is the web composer's message: the Sheet chip with the revision
// the person saw when they allowed the agent to change it.
func (s *chatRunScenario) sendSeen(version string) (error, []ws.ChatEvent) {
	s.t.Helper()
	var events []ws.ChatEvent
	meta := map[string]any{chatbridge.ArtifactRefsMetadataKey: []any{map[string]any{"page_id": "sheet-own", "version": version}}}
	err := s.bridge.HandleChatMessage(s.t.Context(), s.f.a.HumanUserID, s.f.a.ChatID, "Change the number in Data A1 to 25", func(e ws.ChatEvent) { events = append(events, e) }, ws.ChatMessageOption{Metadata: meta})
	return err, events
}

// someoneSaves stands for a save from another tab, a restore or another
// editor: the Sheet's latest revision moves on.
func (s *chatRunScenario) someoneSaves() {
	s.t.Helper()
	mustExec(s.t, s.f.rig.db, `UPDATE artifact_version_heads SET latest_version=latest_version+1 WHERE page_id='sheet-own'`)
}

func (s *chatRunScenario) agentApply(call func(string, any) (string, bool, json.RawMessage), generation, base, request string) (string, bool) {
	s.t.Helper()
	text, isError, _ := call("tools/call", map[string]any{"name": "craft_sheet_apply", "arguments": map[string]any{"generation": generation, "base_version": base, "request_id": request,
		"commands": []any{map[string]any{"command": "cell.set", "params": map[string]any{"sheet": "Data", "cell": "A1", "input": request}}}}})
	return text, isError
}

func (s *chatRunScenario) agentLease(call func(string, any) (string, bool, json.RawMessage)) string {
	s.t.Helper()
	var lease struct {
		Generation string `json:"generation"`
	}
	if got := s.tool(call, "craft_sheet_acquire", map[string]any{}); json.Unmarshal([]byte(got), &lease) != nil || lease.Generation == "" {
		s.t.Fatalf("acquire: %s", got)
	}
	return lease.Generation
}

func consentOf(t *testing.T, s *chatRunScenario) (int64, bool) {
	t.Helper()
	var v *int64
	if err := s.f.rig.db.QueryRow(`SELECT consent_version FROM artifact_message_refs WHERE page_id='sheet-own' ORDER BY created_at DESC LIMIT 1`).Scan(&v); err != nil {
		return 0, false
	}
	if v == nil {
		return 0, true
	}
	return *v, true
}

func TestChatRunSheetConsentNamesTheRevisionThePersonSaw(t *testing.T) {
	s := newChatRunScenario(t, true)
	if err, _ := s.sendSeen("1"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("the message did not reach a run: %v", err)
	}
	if v, ok := consentOf(t, s); !ok || v != 1 {
		t.Fatalf("consent recorded %d %v, want revision 1", v, ok)
	}
}

func TestChatRunSheetConsentStaleWhenTheSheetMovedBeforeSending(t *testing.T) {
	// Two tabs, or a confirmation that waited: the person allowed revision 1,
	// the Sheet is at 2 when the message is sent.
	s := newChatRunScenario(t, true)
	s.someoneSaves()
	err, events := s.sendSeen("1")
	if !errors.Is(err, chatbridge.ErrArtifactConsentStale) {
		t.Fatalf("send: %v", err)
	}
	found := false
	for _, e := range events {
		if meta, _ := e.Metadata.(map[string]any); e.Type == "error" && meta["reason"] == "consent_stale" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the person was not told consent_stale: %+v", events)
	}
	if _, ok := consentOf(t, s); ok {
		t.Fatal("a reference was recorded for a stale consent")
	}
	if s.resolver.runID != "" {
		t.Fatal("a run started on a stale consent")
	}
	for _, bad := range []string{"0", "-1", "abc", "1.0", ""} {
		if err, _ := s.sendSeen(bad); !errors.Is(err, chatbridge.ErrArtifactRefUnavailable) {
			t.Fatalf("version %q: %v", bad, err)
		}
	}
}

func TestChatRunSheetConsentStaleWhenAPersonSavesBeforeTheFirstWrite(t *testing.T) {
	s := newChatRunScenario(t, true)
	if err, _ := s.sendSeen("1"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("send: %v", err)
	}
	<-s.resolver.admitted
	s.someoneSaves()
	call := s.agent()
	generation := s.agentLease(call)
	for _, base := range []string{"1", "2"} {
		text, isError := s.agentApply(call, generation, base, "first-"+base)
		if !isError || !strings.Contains(text, CraftSheetReasonConsentStale) {
			t.Fatalf("write on %s after the person's save: %s", base, text)
		}
	}
	if s.head() != 2 {
		t.Fatalf("version %d, want the person's 2 untouched", s.head())
	}
}

func TestChatRunSheetConsentCoversTheRunsOwnRevisionsOnly(t *testing.T) {
	s := newChatRunScenario(t, true)
	if err, _ := s.sendSeen("1"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("send: %v", err)
	}
	<-s.resolver.admitted
	call := s.agent()
	generation := s.agentLease(call)
	if text, isError := s.agentApply(call, generation, "1", "w1"); isError {
		t.Fatalf("first write: %s", text)
	}
	if s.head() != 2 {
		t.Fatalf("version %d after the first write", s.head())
	}
	// Not back on the consented revision: only on its own.
	if text, isError := s.agentApply(call, generation, "1", "w1-again"); !isError || !strings.Contains(text, CraftSheetReasonStale) || s.head() != 2 {
		t.Fatalf("write on the consented revision after its own: %s", text)
	}
	// Its own exact retry is the same answer, not a second revision.
	if text, isError := s.agentApply(call, generation, "1", "w1"); isError || s.head() != 2 {
		t.Fatalf("exact retry: %s", text)
	}
	if text, isError := s.agentApply(call, generation, "2", "w2"); isError {
		t.Fatalf("second write on its own revision: %s", text)
	}
	if s.head() != 3 {
		t.Fatalf("version %d after the second write", s.head())
	}
	// A person saves between two writes of the agent.
	s.someoneSaves()
	if text, isError := s.agentApply(call, generation, "3", "w3"); !isError || !strings.Contains(text, CraftSheetReasonConsentStale) {
		t.Fatalf("write after the person's save: %s", text)
	}
	if s.head() != 4 {
		t.Fatalf("version %d, want the person's 4 untouched", s.head())
	}
}

func TestArtifactMessageRefsConsentMigration(t *testing.T) {
	s := newChatRunScenario(t, true)
	if err, _ := s.sendSeen("1"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("send: %v", err)
	}
	var message string
	if err := s.f.rig.db.QueryRow(`SELECT message_id FROM artifact_message_refs LIMIT 1`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	s.seedSheet("sheet-second", s.f.a.HumanUserID)
	insert := func(page string, consent any) error {
		_, err := s.f.rig.db.Exec(`INSERT INTO artifact_message_refs(message_id,workspace_id,human_user_id,chat_id,page_id,created_at,consent_version) VALUES(?,?,?,?,?,'2026-11-26T00:00:00.000Z',?)`,
			message, s.f.a.WorkspaceID, s.f.a.HumanUserID, s.f.a.ChatID, page, consent)
		return err
	}
	if err := insert("sheet-second", 7); err == nil {
		t.Fatal("a consent that is not the Sheet's latest revision was stored")
	}
	// A binary from before the column names no consent: it still records.
	if _, err := s.f.rig.db.Exec(`INSERT INTO artifact_message_refs(message_id,workspace_id,human_user_id,chat_id,page_id,created_at) VALUES(?,?,?,?,?,'2026-11-26T00:00:00.000Z')`,
		message, s.f.a.WorkspaceID, s.f.a.HumanUserID, s.f.a.ChatID, "sheet-second"); err != nil {
		t.Fatalf("an older binary's reference: %v", err)
	}
	if _, err := s.f.rig.db.Exec(`UPDATE artifact_message_refs SET consent_version=1 WHERE page_id='sheet-second'`); err == nil {
		t.Fatal("a recorded consent changed")
	}
	if err := insert("sheet-own", 0); err == nil {
		t.Fatal("consent 0 stored")
	}
}

func TestChatRunSheetNullConsentWritesNothing(t *testing.T) {
	// A reference recorded by a binary from before consent revisions: the agent
	// may read but not write.
	s := newChatRunScenario(t, true)
	if err, _ := s.sendSeen("1"); !errors.Is(err, errScenarioModel) {
		t.Fatalf("send: %v", err)
	}
	<-s.resolver.admitted
	var message string
	if err := s.f.rig.db.QueryRow(`SELECT message_id FROM artifact_message_refs LIMIT 1`).Scan(&message); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.f.rig.db, `DROP TRIGGER artifact_message_refs_immutable`)
	mustExec(t, s.f.rig.db, `UPDATE artifact_message_refs SET consent_version=NULL`)
	call := s.agent()
	generation := s.agentLease(call)
	if text, isError := s.agentApply(call, generation, "1", "null"); !isError || !strings.Contains(text, CraftSheetReasonConsentStale) {
		t.Fatalf("write on a NULL consent: %s", text)
	}
	if s.head() != 1 {
		t.Fatalf("version %d", s.head())
	}
}

// agent-access names the latest confirmed revision the agent would start from,
// allowed or not: its number, time and the author's name, as the history does.
func TestChatRunSheetAgentAccessNamesTheHead(t *testing.T) {
	s := newChatRunScenario(t, true)
	db := s.f.rig.db
	var actor, at, name string
	if err := db.QueryRow(`SELECT v.actor, v.created_at FROM artifact_versions v JOIN artifact_version_heads h ON h.page_id=v.page_id AND h.latest_version=v.version WHERE v.page_id='sheet-own'`).Scan(&actor, &at); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(NULLIF(full_name,''),'User') FROM users WHERE id=?`, strings.TrimPrefix(actor, "user:")).Scan(&name); err != nil {
		t.Fatal(err)
	}
	out := s.access()
	if out.HeadVersion != "1" || out.SavedAt != at || out.SavedByName != name {
		t.Fatalf("head %+v, want 1 %s %s", out, at, name)
	}
	// An agent's revision is named by the agent; a refusal still names it.
	mustExec(t, db, `INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,actor,created_at) SELECT workspace_id,page_id,2,manifest_json,'agent:'||?,'2026-11-26T08:00:00Z' FROM artifact_versions WHERE page_id='sheet-own' AND version=1`, s.f.a.AgentID)
	mustExec(t, db, `UPDATE artifact_version_heads SET latest_version=2 WHERE page_id='sheet-own'`)
	mustExec(t, db, `UPDATE agents SET name='Tabulkář' WHERE id=?`, s.f.a.AgentID)
	out = s.access()
	if out.HeadVersion != "2" || out.SavedAt != "2026-11-26T08:00:00Z" || out.SavedByName != "Tabulkář" {
		t.Fatalf("after an agent revision %+v", out)
	}
	// With the agent refused (feature off), the head is still named.
	off := newChatRunScenario(t, false)
	if out = off.access(); out.Allowed || out.HeadVersion != "1" || out.SavedByName == "" {
		t.Fatalf("refused access %+v", out)
	}
}

// default_allowed starts the consent switch on for a Sheet the person brought into this very chat
// (uploaded or attached here), only where the agent would be allowed; it permits nothing itself.
func TestChatRunSheetAgentAccessDefaultsOnForASheetOfThisChat(t *testing.T) {
	s := newChatRunScenario(t, true)
	db := s.f.rig.db
	if out := s.access(); !out.Allowed || out.DefaultAllowed {
		t.Fatalf("before the Sheet came into the chat %+v", out)
	}
	mustExec(t, db, `INSERT INTO artifact_chat_refs(chat_id,page_id,workspace_id,human_user_id,created_at) SELECT id,'sheet-own',workspace_id,created_by,'2026-11-26T08:00:00Z' FROM chats WHERE id=?`, s.f.a.ChatID)
	if out := s.access(); !out.Allowed || !out.DefaultAllowed {
		t.Fatalf("uploaded in this chat %+v", out)
	}
	off := newChatRunScenario(t, false)
	mustExec(t, off.f.rig.db, `INSERT INTO artifact_chat_refs(chat_id,page_id,workspace_id,human_user_id,created_at) SELECT id,'sheet-own',workspace_id,created_by,'2026-11-26T08:00:00Z' FROM chats WHERE id=?`, off.f.a.ChatID)
	if out := off.access(); out.Allowed || out.DefaultAllowed {
		t.Fatalf("refused agent %+v", out)
	}
}
