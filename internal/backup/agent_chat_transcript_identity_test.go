package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptSemanticComparisonPreservesOriginalInode(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "conversations"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "conversations", "chat.jsonl")
	original := "{\"id\":\"msg\", \"session_id\":\"chat\",\"metadata\":{\"z\":2,\"a\":9007199254740993}}\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	payload := transcriptPayload(t, map[string]string{"chat": "{\"metadata\":{\"a\":9007199254740993,\"z\":2},\"session_id\":\"chat\",\"id\":\"msg\"}\n"})
	state, err := prepareAgentChatTranscripts(t.Context(), root, payload, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.publish(); err != nil {
		t.Fatal(err)
	}
	if err := state.finish(true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	got, _ := os.ReadFile(path)
	if !os.SameFile(before, after) || string(got) != original {
		t.Fatalf("semantic retry replaced original: %s", got)
	}
}

func TestTranscriptForkRemapsPageAndReconcilesOriginalUser(t *testing.T) {
	payload := transcriptPayload(t, map[string]string{"source": "{\"session_id\":\"source\",\"id\":\"trigger\",\"run_id\":\"run\",\"author_user_id\":\"source-user\",\"metadata\":{\"page_context\":{\"workspace_id\":\"source-ws\",\"page_id\":\"source-page\",\"block_id\":\"block-kept\"},\"additive\":9007199254740993}}\n"})
	payload.DBDump.AgentChatTranscripts["source"] = "fork"
	payload.DBDump.Tables["chats"][0]["id"] = "fork"
	payload.DBDump.chatTranscriptRemap = map[string]map[string]string{
		"users": {"source-user": "fork-user"}, "workspaces": {"source-ws": "fork-ws"}, "pages": {"source-page": "fork-page"},
	}
	reconcileTranscriptUsers(payload.DBDump, map[string]string{"fork-user": "existing-target-user"})
	root := t.TempDir()
	state, err := prepareAgentChatTranscripts(t.Context(), root, payload, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.publish(); err != nil {
		t.Fatal(err)
	}
	if err := state.finish(true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "conversations", "fork.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Session  string `json:"session_id"`
		Author   string `json:"author_user_id"`
		ID       string `json:"id"`
		Run      string `json:"run_id"`
		Metadata struct {
			Additive json.Number `json:"additive"`
			Page     struct {
				Workspace string `json:"workspace_id"`
				Page      string `json:"page_id"`
				Block     string `json:"block_id"`
			} `json:"page_context"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Session != "fork" || msg.Author != "existing-target-user" || msg.ID != "trigger" || msg.Run != "run" || msg.Metadata.Page.Workspace != "fork-ws" || msg.Metadata.Page.Page != "fork-page" || msg.Metadata.Page.Block != "block-kept" || msg.Metadata.Additive != "9007199254740993" {
		t.Fatalf("remap lost context: %s", raw)
	}
}
