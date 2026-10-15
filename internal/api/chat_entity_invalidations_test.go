package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// A sentinel on the same hub queue proves all preceding publications have been
// dispatched; absence checks need no arbitrary sleep.
func entityHintFrames(t *testing.T, hub *ws.Hub, observer *ws.Observer, chat string) []ws.ServerMessage {
	t.Helper()
	hub.BroadcastChannel("session", chat, "test.sentinel", nil)
	var frames []ws.ServerMessage
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-observer.Frames():
			var frame ws.ServerMessage
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Type == "test.sentinel" {
				return frames
			}
			if frame.Type == "chat.entity_refs_changed" || frame.Type == "issue.revision_changed" {
				frames = append(frames, frame)
			}
		case <-deadline:
			t.Fatal("entity invalidation sentinel missing")
		}
	}
}

func TestChatEntityInvalidationsCommitReplayRollback(t *testing.T) {
	f := newChatIssueFixture(t)
	hub := startedTestHub(t)
	f.h.hub = hub
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.h.db))
	allowed, err := hub.CanSubscribeChannel(t.Context(), f.user, "session:"+f.chat)
	if err != nil || !allowed {
		t.Fatalf("legitimate subscriber denied: %v", err)
	}
	observer := hub.AddObserver("session:"+f.chat, f.user, 16)
	defer hub.RemoveObserver("session:"+f.chat, observer)
	body := f.createBody("hint-create", "Private title must not enter hint")
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 201)
	frames := entityHintFrames(t, hub, observer, f.chat)
	if len(frames) != 2 {
		t.Fatalf("commit hints: %+v", frames)
	}
	for i, name := range []string{"invalidation.json", "revision-invalidation.json"} {
		frames[i].Channel = "session:<chat id>"
		actualRaw, _ := json.Marshal(frames[i])
		expectedRaw, err := os.ReadFile("testdata/chat-entity-refs/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var actual, expected any
		if err = json.Unmarshal(actualRaw, &actual); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(expectedRaw, &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("hint contract: %s", actualRaw)
		}
	}
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 200)
	if frames := entityHintFrames(t, hub, observer, f.chat); len(frames) != 0 {
		t.Fatalf("replay published: %+v", frames)
	}
	body["title"] = "Conflicting replay"
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 409)
	if frames := entityHintFrames(t, hub, observer, f.chat); len(frames) != 0 {
		t.Fatalf("conflict published: %+v", frames)
	}
	execOrFatal(t, f.h.db, `CREATE TRIGGER reject_hint_ref BEFORE INSERT ON chat_entity_refs BEGIN SELECT RAISE(ABORT,'injected card failure'); END`)
	body = f.createBody("hint-rollback", "Rolled back")
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 500)
	if frames := entityHintFrames(t, hub, observer, f.chat); len(frames) != 0 {
		t.Fatalf("rollback published: %+v", frames)
	}
	chatIssueCount(t, f, "missions", 1)
	chatIssueCount(t, f, "chat_entity_refs", 1)
}

func TestChatEntityInvalidationsOnlyReferencedSessions(t *testing.T) {
	f := newChatIssueFixture(t)
	created := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", f.createBody("hint-scope", "Scoped")), 201)
	hub := startedTestHub(t)
	f.h.hub = hub
	source := hub.AddObserver("session:"+f.chat, f.user, 8)
	defer hub.RemoveObserver("session:"+f.chat, source)
	other := hub.AddObserver("session:unrelated", f.user, 8)
	defer hub.RemoveObserver("session:unrelated", other)
	workspace := hub.AddObserver("workspace:"+f.workspace, f.user, 8)
	defer hub.RemoveObserver("workspace:"+f.workspace, workspace)
	broadcastIssueCardInvalidations(t.Context(), f.h.db, hub, f.workspace, created["id"].(string), false)
	if frames := entityHintFrames(t, hub, source, f.chat); len(frames) != 1 || frames[0].Type != "issue.revision_changed" {
		t.Fatalf("source: %+v", frames)
	}
	if frames := entityHintFrames(t, hub, other, "unrelated"); len(frames) != 0 {
		t.Fatalf("unrelated: %+v", frames)
	}
	select {
	case frame := <-workspace.Frames():
		t.Fatalf("private issue hint reached workspace: %s", frame)
	default:
	}
	broadcastIssueCardInvalidations(t.Context(), f.h.db, hub, "foreign-workspace", created["id"].(string), true)
	if frames := entityHintFrames(t, hub, source, f.chat); len(frames) != 0 {
		t.Fatalf("cross-workspace: %+v", frames)
	}
}

func TestChatEntityInvalidationsDeletionCommitAndRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			f := setupDeletionFixture(t)
			hub := startedTestHub(t)
			f.h.hub = hub
			observers := map[string]*ws.Observer{}
			for _, chat := range []string{"delete-chat-one", "delete-chat-two"} {
				observer := hub.AddObserver("session:"+chat, f.user, 8)
				observers[chat] = observer
				defer hub.RemoveObserver("session:"+chat, observer)
			}
			wantStatus, wantHints := 204, 2
			if rollback {
				execOrFatal(t, f.h.db, `CREATE TRIGGER reject_hint_delete BEFORE INSERT ON chat_entity_refs WHEN EXISTS(SELECT 1 FROM agent_operations WHERE id=NEW.operation_id AND verb='deleted') BEGIN SELECT RAISE(ABORT,'injected deletion failure'); END`)
				wantStatus, wantHints = 500, 0
			}
			if rr := f.delete(); rr.Code != wantStatus {
				t.Fatalf("delete status=%d %s", rr.Code, rr.Body.String())
			}
			for chat, observer := range observers {
				if frames := entityHintFrames(t, hub, observer, chat); len(frames) != wantHints {
					t.Fatalf("deletion hints in %s: %+v", chat, frames)
				}
			}
		})
	}
}

func TestChatEntityInvalidationsShowDoesNotAnnounceOtherChatActivity(t *testing.T) {
	f := newChatIssueFixture(t)
	seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "BACKLOG")
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, "other-chat", "other-run")
	body := map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": "other-run", "operation_key": "show-other"}
	chatIssueJSON(t, chatIssueCall(t, f.h.Show, "POST", "ENG-1", body), 201)
	hub := startedTestHub(t)
	f.h.hub = hub
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.h.db))
	a := hub.AddObserver("session:"+f.chat, f.user, 16)
	defer hub.RemoveObserver("session:"+f.chat, a)
	b := hub.AddObserver("session:other-chat", f.user, 16)
	defer hub.RemoveObserver("session:other-chat", b)
	body["author_run_id"] = f.run
	body["operation_key"] = "show-current"
	chatIssueJSON(t, chatIssueCall(t, f.h.Show, "POST", "ENG-1", body), 201)
	if frames := entityHintFrames(t, hub, a, f.chat); len(frames) != 1 || frames[0].Type != "chat.entity_refs_changed" {
		t.Fatalf("show origin: %+v", frames)
	}
	if frames := entityHintFrames(t, hub, b, "other-chat"); len(frames) != 0 {
		t.Fatalf("show disclosed to other chat: %+v", frames)
	}
}

func TestChatEntityInvalidationsCommentOnlyAnnouncesOriginReferences(t *testing.T) {
	f := newChatIssueFixture(t)
	seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "BACKLOG")
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, "other-chat", "other-run")
	body := map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": "other-run", "operation_key": "show-other"}
	chatIssueJSON(t, chatIssueCall(t, f.h.Show, "POST", "ENG-1", body), 201)
	hub := startedTestHub(t)
	f.h.hub = hub
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.h.db))
	a := hub.AddObserver("session:"+f.chat, f.user, 16)
	defer hub.RemoveObserver("session:"+f.chat, a)
	b := hub.AddObserver("session:other-chat", f.user, 16)
	defer hub.RemoveObserver("session:other-chat", b)
	body["author_run_id"] = f.run
	body["operation_key"] = "comment-current"
	body["body"] = "Changed here"
	chatIssueJSON(t, chatIssueCall(t, f.h.CreateComment, "POST", "ENG-1", body), 201)
	if frames := entityHintFrames(t, hub, a, f.chat); len(frames) != 2 || frames[0].Type != "chat.entity_refs_changed" || frames[1].Type != "issue.revision_changed" {
		t.Fatalf("comment origin: %+v", frames)
	}
	if frames := entityHintFrames(t, hub, b, "other-chat"); len(frames) != 1 || frames[0].Type != "issue.revision_changed" {
		t.Fatalf("comment disclosed activity to other chat: %+v", frames)
	}
}

func TestChatEntityInvalidationsReviewPolicyAndTakeover(t *testing.T) {
	for _, action := range []string{"review-policy", "take-over"} {
		t.Run(action, func(t *testing.T) {
			f := newChatIssueFixture(t)
			seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "TODO")
			chatIssueJSON(t, chatIssueCall(t, f.h.Show, "POST", "ENG-1", map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": f.run, "operation_key": "show"}), 201)
			hub := startedTestHub(t)
			hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.h.db))
			observer := hub.AddObserver("session:"+f.chat, f.user, 16)
			defer hub.RemoveObserver("session:"+f.chat, observer)
			handler := NewIssueHandler(f.h.db, hub, nil, newTestLogger())
			rr := httptest.NewRecorder()
			if action == "review-policy" {
				handler.ReviewPolicy(rr, covIWReq(f.user, f.workspace, "OWNER", "PUT", `{"revision":0,"client_review_required":false}`, f.crew, "ENG-1"))
			} else {
				handler.Work(rr, covIWReq(f.user, f.workspace, "OWNER", "POST", `{"revision":0,"operation_id":"take-hint","action":"take_over"}`, f.crew, "ENG-1"))
			}
			if rr.Code != 200 {
				t.Fatalf("mutation: %d %s", rr.Code, rr.Body.String())
			}
			if frames := entityHintFrames(t, hub, observer, f.chat); len(frames) != 1 || frames[0].Type != "issue.revision_changed" {
				t.Fatalf("mutation hints: %+v", frames)
			}
		})
	}
}

func TestChatEntityInvalidationsAgentRelations(t *testing.T) {
	for _, relation := range []string{"relates_to", "sub_issue_of"} {
		t.Run(relation, func(t *testing.T) {
			f := newChatIssueFixture(t)
			for _, ident := range []string{"ENG-1", "ENG-2"} {
				seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, ident, "BACKLOG")
				chatIssueJSON(t, chatIssueCall(t, f.h.Show, "POST", ident, map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": f.run, "operation_key": "show-" + ident}), 201)
			}
			hub := startedTestHub(t)
			f.h.hub = hub
			hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(f.h.db))
			observer := hub.AddObserver("session:"+f.chat, f.user, 16)
			defer hub.RemoveObserver("session:"+f.chat, observer)
			rr := chatIssueCall(t, f.h.CreateRelation, "POST", "ENG-1", map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "target_identifier": "ENG-2", "relation_type": relation})
			if rr.Code != 201 {
				t.Fatalf("relation: %d %s", rr.Code, rr.Body.String())
			}
			if frames := entityHintFrames(t, hub, observer, f.chat); len(frames) != 2 || frames[0].Type != "issue.revision_changed" || frames[1].Type != "issue.revision_changed" {
				t.Fatalf("endpoint hints: %+v", frames)
			}
		})
	}
}
