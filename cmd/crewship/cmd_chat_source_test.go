package main

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestChatSourceRejectsInvalidAndSyntheticPagingBeforeRequest(t *testing.T) {
	guardCLIState(t)
	for _, args := range [][]string{{"messages", "unknown", "chat"}, {"messages", "agent_session", "chat", "--limit", "10"}, {"messages", "agent_session", "chat", "--after-sequence", "0"}, {"messages", "workspace_room", "room", "--limit", "101"}, {"messages", "workspace_room", "room", "--before-sequence", "0"}, {"messages", "workspace_room", "room", "--before-sequence", "2", "--after-sequence", "1"}} {
		calls := 0
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
		setStubCLI(t, s.URL)
		c := newChatSourceCommand()
		c.SilenceErrors = true
		c.SilenceUsage = true
		c.SetArgs(args)
		if err := c.Execute(); err == nil || calls != 0 {
			t.Fatalf("invalid command accepted: %v", args)
		}
		s.Close()
	}
}
func TestChatSourceRejectsOldServerProjection(t *testing.T) {
	guardCLIState(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("projection") != "source_v1" {
			t.Error("projection selector missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer s.Close()
	setStubCLI(t, s.URL)
	c := newChatSourceCommand()
	c.SetArgs([]string{"messages", "agent_session", "chat"})
	if err := c.Execute(); err == nil {
		t.Fatal("old server silently accepted")
	}
}

func TestChatSourcePreservesNativeIdentityAndRoomCursor(t *testing.T) {
	guardCLIState(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/conversations/room/messages" || r.URL.Query().Get("projection") != "source_v1" || r.URL.Query().Get("after_sequence") != "7" || r.URL.Query().Get("limit") != "10" {
			t.Errorf("wrong native request %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": 1, "conversation_ref": map[string]string{"workspace_id": covWS, "source_kind": "workspace_room", "source_id": "room"}, "messages": []any{}, "has_more": true})
	}))
	defer s.Close()
	setStubCLI(t, s.URL)
	flagFormat = "json"
	c := newChatSourceCommand()
	c.SetArgs([]string{"messages", "workspace_room", "room", "--after-sequence", "7", "--limit", "10"})
	output := captureStdoutCovCli2(t, func() {
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, `"has_more": true`) || !strings.Contains(output, `"workspace_id": "`+covWS+`"`) {
		t.Fatalf("source envelope lost: %s", output)
	}
}

// This is a local real-router/binary contract, not a provider/model run.
func TestChatSourceBinaryReadsOriginalRoomMessage(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "csourceworkspace00000001"
	const token = "crewship_cli_source_source_source_source"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug)VALUES('csourceworkspace00000001','Sources','sources')`,
		`INSERT INTO users(id,email,full_name)VALUES('source-owner','source@example.invalid','Source owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role)VALUES('source-member','csourceworkspace00000001','source-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at)VALUES('source-token','source-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	store := groupchat.New(db)
	room, err := store.Create(t.Context(), workspace, "source-owner", groupchat.CreateInput{Title: "Original source", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.Send(t.Context(), workspace, "source-owner", room.ID, groupchat.SendInput{ClientID: "original-send", Content: "Original human message"})
	if err != nil {
		t.Fatal(err)
	}
	// The agent route uses the real JSONL store through a bounded Unix IPC
	// fixture. This does not start a Crewship server or a provider/model run.
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode)VALUES('csourcecrew000000000001','csourceworkspace00000001','Sources','source-crew','free')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug)VALUES('csourceagent00000000001','csourcecrew000000000001','csourceworkspace00000001','Source agent','source-agent')`,
		`INSERT INTO chats(id,agent_id,workspace_id,created_by,title)VALUES('source-agent-chat','csourceagent00000000001','csourceworkspace00000001','source-owner','Source transcript')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	transcriptDir := t.TempDir()
	transcript := conversation.NewStore(transcriptDir, slog.Default())
	t.Cleanup(transcript.Close)
	original := conversation.Message{ID: "original-agent-message", AgentID: "csourceagent00000000001", Role: conversation.RoleAssistant, Content: "Original agent reply", Timestamp: time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC), Parts: []conversation.Part{{Type: "text", Content: "Original agent reply"}, {Type: "thinking", Content: "private legacy thinking fixture", Metadata: map[string]any{"private": "legacy metadata fixture"}}}}
	if err := transcript.Append(t.Context(), "source-agent-chat", original); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(transcriptDir, "conversations", "source-agent-chat.jsonl")
	beforeTranscript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	ipcDir, err := os.MkdirTemp("/tmp", "source-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(ipcDir); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(ipcDir, "ipc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var ipcCalls atomic.Int64
	ipc := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ipcCalls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/chats/source-agent-chat/shared-messages" {
			t.Error("unexpected IPC route")
			http.NotFound(w, r)
			return
		}
		rows, err := transcript.ReadShared(r.Context(), "source-agent-chat", 16<<20, 1000)
		if err != nil {
			t.Error("owned bounded transcript read failed")
			http.Error(w, "Source unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"messages": rows}); err != nil {
			t.Error("IPC response encoding failed")
		}
	})}
	ipcDone := make(chan error, 1)
	go func() { ipcDone <- ipc.Serve(listener) }()
	t.Cleanup(func() {
		_ = ipc.Close()
		select {
		case err := <-ipcDone:
			if err != nil && err != http.ErrServerClosed {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("owned IPC fixture did not stop")
		}
	})
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), api.WithSocketPath(socket))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	command := exec.Command(binary, "chat", "source", "messages", "workspace_room", room.ID, "--after-sequence", "0", "--limit", "10")
	command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real source CLI failed: %v %s", err, output)
	}
	var body struct {
		Version  int `json:"contract_version"`
		Messages []struct {
			Ref struct {
				ID string `json:"source_message_id"`
			} `json:"message_ref"`
			Order struct {
				Sequence int64 `json:"source_sequence"`
			} `json:"order"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(output, &body); err != nil || body.Version != 1 || len(body.Messages) != 1 || body.Messages[0].Ref.ID != message.ID || body.Messages[0].Order.Sequence != message.Sequence {
		t.Fatalf("original source identity lost: %v %s", err, output)
	}
	var runsBefore int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type LIKE 'run.%'`).Scan(&runsBefore); err != nil {
		t.Fatal(err)
	}
	runAgent := func() ([]byte, error) {
		command := exec.Command(binary, "chat", "source", "messages", "agent_session", "source-agent-chat")
		command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		return command.CombinedOutput()
	}
	t.Run("agent_session", func(t *testing.T) {
		output, err := runAgent()
		if err != nil {
			t.Fatalf("real agent source CLI failed: %v", err)
		}
		var body conversation.SourceMessagesResponse
		if err := json.Unmarshal(output, &body); err != nil {
			t.Fatal("agent source CLI did not return JSON")
		}
		if body.ContractVersion != 1 || body.ConversationRef.WorkspaceID != workspace || body.ConversationRef.SourceKind != conversation.SourceAgentSession || body.ConversationRef.SourceID != "source-agent-chat" || len(body.Messages) != 1 || body.Messages[0].MessageRef == nil || body.Messages[0].MessageRef.SourceMessageID != original.ID || body.Messages[0].Order.SourcePosition == nil || *body.Messages[0].Order.SourcePosition != 0 || body.Messages[0].Order.SourceSequence != nil || body.SnapshotComplete == nil || !*body.SnapshotComplete {
			t.Fatal("original agent identity or bounded snapshot contract lost")
		}
		if strings.Contains(string(output), "private legacy thinking fixture") || strings.Contains(string(output), "legacy metadata fixture") {
			t.Fatal("agent source projection exposed legacy thinking")
		}
		if ipcCalls.Load() != 1 {
			t.Fatal("agent source read did not use exactly one bounded IPC read")
		}
		if _, err := db.Exec(`DELETE FROM cli_tokens WHERE id='source-token'`); err != nil {
			t.Fatal(err)
		}
		denied, err := runAgent()
		if err == nil || strings.Contains(string(denied), original.Content) || ipcCalls.Load() != 1 {
			t.Fatal("revoked current credential reached source transcript")
		}
		afterTranscript, err := os.ReadFile(transcriptPath)
		if err != nil || string(beforeTranscript) != string(afterTranscript) {
			t.Fatal("source read or denial rewrote the original transcript")
		}
		var runsAfter int
		if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type LIKE 'run.%'`).Scan(&runsAfter); err != nil || runsAfter != runsBefore {
			t.Fatal("source read created agent execution")
		}
	})
	var jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_conversation_agent_jobs WHERE conversation_id=?`, room.ID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("source read created agent work")
	}
}
