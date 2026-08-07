package main

// POST /api/v1/agents/{agentId}/chats had no CLI command of its own. Every
// caller reached it on the way to a model call — `ask`, `run`, `routine
// iterate` — so on an instance with no provider credential there was no way to
// end up with a chat at all, and the two Tier A sections of
// scripts/test-harness/test-run-stream.sh (both of which need a chat that
// genuinely EXISTS) skipped on every fresh CI instance (#1829).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const chatCreateAgentID = "cagent00000000000001"

func chatCreateStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := covStub(t)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": chatCreateAgentID, "slug": "atlas", "name": "Atlas"},
	}))
	return stub
}

func TestChatCreate_ReturnsTheChatIDOnStdout(t *testing.T) {
	stub := chatCreateStub(t)
	stub.OnPost("/api/v1/agents/"+chatCreateAgentID+"/chats",
		clitest.JSONResponse(201, map[string]string{"id": "cchat000000000000001"}))

	covResetFlags(t, chatCreateCmd)
	out := covCaptureStdoutCli5(t, func() {
		if err := chatCreateCmd.RunE(chatCreateCmd, []string{"atlas"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// `CHAT=$(crewship chat create atlas)` has to work — the id on stdout,
	// nothing else on it.
	if strings.TrimSpace(out) != "cchat000000000000001" {
		t.Errorf("stdout = %q, want the bare chat id", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/agents/"+chatCreateAgentID+"/chats")
	if len(calls) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["origin"] != "CLI" {
		t.Errorf("origin = %q, want CLI", body["origin"])
	}
}

func TestChatCreate_JSONFormat(t *testing.T) {
	stub := chatCreateStub(t)
	stub.OnPost("/api/v1/agents/"+chatCreateAgentID+"/chats",
		clitest.JSONResponse(201, map[string]string{"id": "cchat000000000000002"}))

	covResetFlags(t, chatCreateCmd)
	origFormat := flagFormat
	t.Cleanup(func() { flagFormat = origFormat })
	flagFormat = "json"

	out := covCaptureStdoutCli5(t, func() {
		if err := chatCreateCmd.RunE(chatCreateCmd, []string{"atlas"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got["id"] != "cchat000000000000002" || got["agent_id"] != chatCreateAgentID {
		t.Errorf("json = %v", got)
	}
}

// The server answering 201 with no id is a broken contract, not a chat.
func TestChatCreate_EmptyIDIsAnError(t *testing.T) {
	stub := chatCreateStub(t)
	stub.OnPost("/api/v1/agents/"+chatCreateAgentID+"/chats",
		clitest.JSONResponse(201, map[string]string{}))

	covResetFlags(t, chatCreateCmd)
	err := chatCreateCmd.RunE(chatCreateCmd, []string{"atlas"})
	if err == nil || !strings.Contains(err.Error(), "no id") {
		t.Errorf("got %v", err)
	}
}

func TestChatCreate_APIErrorPropagates(t *testing.T) {
	stub := chatCreateStub(t)
	stub.OnPost("/api/v1/agents/"+chatCreateAgentID+"/chats", func(*http.Request, []byte) (int, []byte, string) {
		return 404, []byte(`{"error":"Agent not found"}`), "application/json"
	})

	covResetFlags(t, chatCreateCmd)
	err := chatCreateCmd.RunE(chatCreateCmd, []string{"atlas"})
	if err == nil || !strings.Contains(err.Error(), "Agent not found") {
		t.Errorf("got %v", err)
	}
}

// Rule #3: the command has to be discoverable in the manifest the harness (and
// any agent) reads, not just in --help text.
func TestChatCreate_IsRegisteredUnderChat(t *testing.T) {
	t.Parallel()
	for _, c := range chatCmd.Commands() {
		if c.Name() == "create" {
			return
		}
	}
	t.Fatal("chat create is not registered under chatCmd")
}
