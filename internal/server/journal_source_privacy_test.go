package server

import (
	"context"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestVerifiedTextJournalBridgeSuppressesWorkspaceProjection(t *testing.T) {
	db := testutil.MigratedDB(t)
	for _, statement := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('source-ws','Workspace','source-ws')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('source-agent','source-ws','Agent','source-agent')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('source-crew','source-ws','Crew','source-crew')`,
		`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,type,source_run_id,created_at) VALUES('private-source','source-ws','source-crew','private-chat','source-agent','private title','TEXT','verified-run','2026-10-16T12:00:00Z'),('legacy-source','source-ws','source-crew','legacy-chat','source-agent','legacy title','TEXT',NULL,'2026-10-16T12:00:00Z'),('credential-source','source-ws','source-crew','private-chat','source-agent','credential title','CREDENTIAL','verified-run','2026-10-16T12:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	broadcaster := newFakeBroadcaster()
	bridge := newJournalWSBridgeWithFilter(broadcaster, nil, func(entry journal.Entry) bool { return allowBroadSourceJournal(context.Background(), db.DB, entry) })
	entries := []journal.Entry{}
	for _, id := range []string{"private-source", "legacy-source", "credential-source"} {
		entries = append(entries, journal.Entry{ID: id, WorkspaceID: "source-ws", Type: journal.EntryPeerEscalation, Summary: id, Refs: map[string]any{"escalation_id": id}})
	}
	// A following legacy entry proves the drain passed the withheld private row.
	bridge.observe(entries)
	broadcaster.waitForCalls(t, 2)
	calls := broadcaster.snapshot()
	if len(calls) != 2 {
		t.Fatalf("unexpected broad frames: %+v", calls)
	}
	for i, expected := range []string{"legacy-source", "credential-source"} {
		payload := calls[i].payload.(map[string]any)
		if payload["id"] != expected {
			t.Fatalf("private source broad journal frame: %+v", payload)
		}
	}
}

func TestVerifiedApprovalJournalBridgeSurvivesSourceHardDelete(t *testing.T) {
	db := testutil.MigratedDB(t)
	for _, statement := range []string{`INSERT INTO workspaces(id,name,slug) VALUES('approval-ws','Approval','approval-ws')`, `INSERT INTO approvals_queue(id,workspace_id,requested_by,kind,reason,payload,status,created_at) VALUES('private-approval','approval-ws','agent','custom','Private approval reason','{"tool":"agent_run","gate_mode":"sync","correlation":{"trigger_type":"human_chat","run_id":"host-run","chat_id":"private-chat","trigger_message_id":"trigger"}}','pending','2026-10-16T12:00:00Z'),('legacy-approval','approval-ws','agent','custom','Legacy approval reason','{}','pending','2026-10-16T12:00:00Z')`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	entry := journal.Entry{ID: "private-approval-event", WorkspaceID: "approval-ws", Type: journal.EntryApprovalRequest, Summary: "Private approval reason", Refs: map[string]any{"approval_id": "private-approval"}}
	if allowBroadSourceJournal(t.Context(), db.DB, entry) {
		t.Fatal("verified approval escaped broad journal bridge")
	}
	if _, err := harbormaster.DeleteSources(t.Context(), db.DB, nil, "approval-ws", "aq.id=?", "private-approval"); err != nil {
		t.Fatal(err)
	}
	if allowBroadSourceJournal(t.Context(), db.DB, entry) {
		t.Fatal("hard delete declassified approval journal event")
	}
	entry.Refs["approval_id"] = "legacy-approval"
	if !allowBroadSourceJournal(t.Context(), db.DB, entry) {
		t.Fatal("legacy approval hidden")
	}
	entry.Type = journal.EntryApprovalSourceRetired
	entry.ActorType = journal.ActorSystem
	entry.Refs["approval_id"] = "private-approval"
	if allowBroadSourceJournal(t.Context(), db.DB, entry) {
		t.Fatal("retirement marker escaped broad journal bridge")
	}
}

func TestVerifiedApprovalBridgePublishesOnlyCurrentAuthorizedChatHint(t *testing.T) {
	db := testutil.MigratedDB(t)
	for _, statement := range []string{`INSERT INTO workspaces(id,name,slug) VALUES('hint-ws','Hint','hint-ws')`, `INSERT INTO users(id,email) VALUES('hint-owner','hint-owner@example.test'),('hint-hidden','hint-hidden@example.test')`, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('hint-owner-member','hint-ws','hint-owner','OWNER','trusted'),('hint-hidden-member','hint-ws','hint-hidden','MEMBER','trusted')`, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('hint-crew','hint-ws','Hint crew','hint-crew')`, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('hint-agent','hint-ws','hint-crew','Hint agent','hint-agent')`, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('hint-chat','hint-ws','hint-agent','hint-owner','private')`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db.DB, "hint-chat", "hint-owner", nil)
	if err != nil || receipt == nil {
		t.Fatalf("source receipt%v %v", receipt, err)
	}
	raw, _ := json.Marshal(map[string]any{"trigger_type": "USER", "chat_id": "hint-chat", "trigger_message_id": "hint-trigger", "human_authority": receipt})
	if _, err = db.Exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('hint-origin','hint-ws','hint-agent','hint-run','run.started','orchestrator','Host start',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	hub := ws.NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(db.DB))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	chatChannel := "chat-hints:hint-chat"
	journalChannel := "journal:hint-ws"
	if allowed, err := hub.CanSubscribeChannel(t.Context(), "hint-hidden", chatChannel); err != nil || allowed {
		t.Fatalf("hidden source subscriber admitted%v %v", allowed, err)
	}
	allowed := hub.AddObserver(chatChannel, "hint-owner", 16)
	defer hub.RemoveObserver(chatChannel, allowed)
	broad := hub.AddObserver(journalChannel, "hint-hidden", 16)
	defer hub.RemoveObserver(journalChannel, broad)
	bridge := newJournalWSBridgeForSources(db.DB, hub, nil)
	writer := journal.NewWriter(db.DB, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{FlushSize: 1})
	defer writer.Close()
	writer.AddReadObserver(bridge.observe)
	id, err := harbormaster.Enqueue(t.Context(), db.DB, writer, harbormaster.Request{WorkspaceID: "hint-ws", CrewID: "hint-crew", AgentID: "hint-agent", RequestedBy: "hint-agent", Kind: harbormaster.KindCustom, Reason: "Private source title", Payload: map[string]any{"tool": "agent_run", "args": map[string]any{}, "gate_mode": "sync", "correlation": map[string]any{"run_id": "hint-run", "chat_id": "hint-chat", "trigger_message_id": "hint-trigger", "trigger_type": "human_chat"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-allowed.Frames():
		var frame ws.ServerMessage
		if json.Unmarshal(raw, &frame) != nil || frame.Type != "chat.entity_refs_changed" {
			t.Fatalf("authorized hint%s", raw)
		}
		payload, _ := json.Marshal(frame.Payload)
		if string(payload) != `{"contract_version":1}` {
			t.Fatalf("hint disclosed source%s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authorized source hint missing")
	}
	// A following public event is a real drain barrier for both hidden-source
	// workspace suppression and no chat hint after generation invalidation.
	barrier := func(label string) {
		t.Helper()
		if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: "hint-ws", Type: journal.EntryApprovalRequest, ActorType: journal.ActorSystem, Summary: label}); err != nil {
			t.Fatal(err)
		}
		if err = writer.Flush(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case raw := <-broad.Frames():
			if !strings.Contains(string(raw), label) || strings.Contains(string(raw), id) {
				t.Fatalf("private workspace frame%s", raw)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("journal drain barrier missing")
		}
	}
	barrier("initial-public-barrier")
	if _, err = db.Exec(`UPDATE chats SET authority_generation='new-generation' WHERE id='hint-chat'`); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Emit(t.Context(), journal.Entry{WorkspaceID: "hint-ws", Type: journal.EntryApprovalRequest, ActorType: journal.ActorAgent, Summary: "Old generation source", Refs: map[string]any{"approval_id": id}}); err != nil {
		t.Fatal(err)
	}
	barrier("generation-public-barrier")
	hub.Broadcast(chatChannel, ws.ServerMessage{Type: "test.sentinel", Channel: chatChannel})
	select {
	case raw := <-allowed.Frames():
		var frame ws.ServerMessage
		_ = json.Unmarshal(raw, &frame)
		if frame.Type != "test.sentinel" {
			t.Fatalf("stale generation announced source%s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("chat barrier missing")
	}
	if _, err = harbormaster.DeleteSources(t.Context(), db.DB, writer, "hint-ws", "aq.id=?", id); err != nil {
		t.Fatal(err)
	}
	barrier("retirement-public-barrier")
	hub.Broadcast(chatChannel, ws.ServerMessage{Type: "test.sentinel", Channel: chatChannel})
	select {
	case raw := <-allowed.Frames():
		var frame ws.ServerMessage
		_ = json.Unmarshal(raw, &frame)
		if frame.Type != "test.sentinel" {
			t.Fatalf("retired source announced%s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retired chat barrier missing")
	}
}
