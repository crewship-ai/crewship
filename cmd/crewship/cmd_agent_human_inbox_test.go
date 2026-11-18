package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/featureflags"
)

func TestAgentHumanInboxCLISendUsesCanonicalJoinedTargetAndWorkspace(t *testing.T) {
	stub := covStub(t)
	cmd := newAgentInboxSendCmd()
	stub.OnPost("/api/v1/conversations/channel1/messages", clitest.JSONResponse(201, map[string]any{"id": "canonical-message"}))
	for k, v := range map[string]string{"conversation": "channel1", "message": "A real human message", "client-id": "send-once"} {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	covCaptureStdoutCli3(t, func() {
		if err := cmd.RunE(cmd, []string{covAgentIDCli3}); err != nil {
			t.Error(err)
		}
	})
	calls := stub.CallsFor("POST", "/api/v1/conversations/channel1/messages")
	if len(calls) != 1 {
		t.Fatalf("message sent %d times", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	mentions, ok := body["mentioned_agent_ids"].([]any)
	if !ok || len(mentions) != 1 || mentions[0] != covAgentIDCli3 || body["client_id"] != "send-once" || body["content"] != "A real human message" {
		t.Fatalf("wrong canonical send=%+v", body)
	}
	if calls[0].Headers.Get(featureflags.AgentInboxRequestHeader) != "1" {
		t.Fatal("canonical send lost explicit inbox gating")
	}
	if !strings.Contains(calls[0].Query, "workspace_id="+covWSCli3) {
		t.Fatalf("workspace not explicit: %s", calls[0].Query)
	}
}
func TestAgentHumanInboxCLITaskKeepsIssueCreationAndStartGates(t *testing.T) {
	stub := covStub(t)
	cmd := newAgentInboxTaskCmd()
	crew := "ccrew0000000000000000aa"
	stub.OnPost("/api/v1/crews/"+crew+"/issues", clitest.JSONResponse(201, map[string]any{"id": "issue-id", "identifier": "ENG-7"}))
	stub.OnPost("/api/v1/crews/"+crew+"/issues/ENG-7/start", clitest.JSONResponse(202, map[string]any{"status": "accepted"}))
	for k, v := range map[string]string{"crew": crew, "title": "Task title", "instructions": "Frozen task context", "client-id": "task-once"} {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := cmd.RunE(cmd, []string{covAgentIDCli3}); err != nil {
			t.Error(err)
		}
	})
	creates := stub.CallsFor("POST", "/api/v1/crews/"+crew+"/issues")
	starts := stub.CallsFor("POST", "/api/v1/crews/"+crew+"/issues/ENG-7/start")
	if len(creates) != 1 || len(starts) != 1 {
		t.Fatalf("create/start counts=%d/%d", len(creates), len(starts))
	}
	if creates[0].Headers.Get(featureflags.AgentInboxRequestHeader) != "1" || starts[0].Headers.Get(featureflags.AgentInboxRequestHeader) != "1" {
		t.Fatal("explicit inbox create or start lost gating")
	}
	if creates[0].Headers.Get("Idempotency-Key") != "task-once" || starts[0].Headers.Get("Idempotency-Key") != "task-once:start" {
		t.Fatal("canonical retry identity lost")
	}
	var body map[string]any
	if err := json.Unmarshal(creates[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["delegate_agent_id"] != covAgentIDCli3 || body["description"] != "Frozen task context" {
		t.Fatalf("wrong task target/context: %+v", body)
	}
	if !strings.Contains(out, "ENG-7") || !strings.Contains(out, `"read_at": null`) {
		t.Fatalf("receipt invented read or lost issue: %s", out)
	}
}
func TestAgentInboxTableReadsActualConsolidatedPeerVocabulary(t *testing.T) {
	out := covCaptureStdoutCli3(t, func() {
		if err := printInboxTable(map[string]any{"peer_messages": []any{map[string]any{"created_at": "2026-11-18T07:00:00Z", "from_agent_name": "Lead", "question": "Actual peer question"}}}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "Lead") || !strings.Contains(out, "Actual peer question") || strings.Contains(out, "Inbox empty") {
		t.Fatalf("actual API peer row was hidden: %s", out)
	}
}

func TestAgentHumanInboxCLITaskRetryObservesExistingStateWithoutRestart(t *testing.T) {
	for _, tc := range []struct {
		name           string
		startCode      int
		status, target string
		wantSuccess    bool
		wantGet        int
	}{
		{"lost start response retry", 400, "IN_PROGRESS", covAgentIDCli3, true, 1},
		{"already completed", 400, "DONE", covAgentIDCli3, true, 1},
		{"not started", 400, "TODO", covAgentIDCli3, false, 1},
		{"moved target", 400, "IN_PROGRESS", "other-agent", false, 1},
		{"revoked start rights", 403, "IN_PROGRESS", covAgentIDCli3, false, 0},
		{"conflict", 409, "IN_PROGRESS", covAgentIDCli3, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := covStub(t)
			cmd := newAgentInboxTaskCmd()
			crew := "ccrew0000000000000000aa"
			path := "/api/v1/crews/" + crew + "/issues/ENG-7"
			stub.OnPost("/api/v1/crews/"+crew+"/issues", clitest.JSONResponse(201, map[string]any{"id": "issue-id", "identifier": "ENG-7"}))
			stub.OnPost(path+"/start", clitest.JSONResponse(tc.startCode, map[string]any{"error": "not startable"}))
			stub.OnGet(path, clitest.JSONResponse(200, map[string]any{"status": tc.status, "delegate_agent_id": tc.target}))
			for k, v := range map[string]string{"crew": crew, "title": "Task", "instructions": "Context", "client-id": "same-key"} {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatal(err)
				}
			}
			var gotErr error
			out := covCaptureStdoutCli3(t, func() { gotErr = cmd.RunE(cmd, []string{covAgentIDCli3}) })
			if (gotErr == nil) != tc.wantSuccess {
				t.Fatalf("success=%v error=%v output=%s", tc.wantSuccess, gotErr, out)
			}
			if len(stub.CallsFor("GET", path)) != tc.wantGet || len(stub.CallsFor("POST", path+"/start")) != 1 || len(stub.CallsFor("POST", "/api/v1/crews/"+crew+"/issues")) != 1 {
				t.Fatal("recovery changed execution or auth boundary")
			}
			if tc.wantSuccess && (!strings.Contains(out, "observed_existing") || !strings.Contains(out, tc.status)) {
				t.Fatalf("invented start acknowledgement=%s", out)
			}
		})
	}
}
