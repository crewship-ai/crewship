package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/usermodel"
)

func TestFeedbackPreferenceActualPrompt(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('u','prefs@example.test','User')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('w','Workspace','w')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m','w','u','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('c','w','Crew','c')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('a','w','c','Agent','a','AGENT','IDLE','CLAUDE_CODE','CODING',1800,1)`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,title) VALUES('chat','w','a','u','Chat')`,
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content) VALUES('msg','chat','a','assistant','Result')`,
		`INSERT INTO message_feedback(id,workspace_id,chat_id,message_id,signal,reason,user_id) VALUES('feedback-source','w','chat','msg','helpful','Next time, keep answers concise.','u')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	o := New(mockContainerForMemory(map[string]string{}), newMemState(), slog.Default())
	o.SetFeedbackPreferenceReader(func(ctx context.Context, ws, agent, user string) (string, error) {
		return usermodel.ReadFeedbackPreferences(ctx, db, ws, agent, user)
	})
	req := AgentRunRequest{ContainerID: "container", WorkspaceID: "w", AgentID: "a", AgentSlug: "a", OpenedByUserID: "u", ChatVisibility: "private", MemoryEnabled: true}
	assembled := req
	if err := o.assembleSystemPrompt(t.Context(), &assembled); err != nil {
		t.Fatal(err)
	}
	got := assembled.SystemPrompt
	if !strings.Contains(got, "prefers concise answers") || !strings.Contains(got, "feedback-source revision 1") {
		t.Fatalf("actual prompt missing persisted preference: %s", got)
	}
	for _, mutate := range []func(*AgentRunRequest){func(r *AgentRunRequest) { r.OpenedByUserID = "other-reader" }, func(r *AgentRunRequest) { r.AgentID = "other-agent" }, func(r *AgentRunRequest) { r.WorkspaceID = "other-workspace" }, func(r *AgentRunRequest) { r.ChatVisibility = "group" }} {
		wrong := req
		mutate(&wrong)
		if got := o.buildMemoryContext(t.Context(), wrong, 0); strings.Contains(got, "prefers concise answers") {
			t.Fatalf("prompt leaked preference: %s", got)
		}
	}
	if _, err := db.Exec(`DELETE FROM message_feedback WHERE id='feedback-source'`); err != nil {
		t.Fatal(err)
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if got := o.buildMemoryContext(t.Context(), req, 0); strings.Contains(got, "prefers concise answers") {
		t.Fatal("undo retained preference in prompt")
	}
}
