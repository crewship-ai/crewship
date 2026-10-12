package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRoutineStartWaitReceiptAndPrecision(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("workspace_id") != "cworkspace012345678901234" {
			t.Error("workspace lost")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			if !strings.HasSuffix(r.URL.Path, "/pipelines/hello/run") || r.Header.Get("Prefer") != "respond-async" || r.Header.Get("Idempotency-Key") != "fixture-key" {
				t.Errorf("wrong start: %s", r.URL.Path)
			}
			var body struct {
				Inputs json.RawMessage `json:"inputs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !strings.Contains(string(body.Inputs), "9007199254740993") {
				t.Error("input precision lost")
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"run_id":"run-1","status":"RUNNING"}`)
		} else {
			fmt.Fprint(w, `{"id":"run-1","status":"completed","output":{"integer":9007199254740993}}`)
		}
	})
	s.allowWrite = true
	session := testMCPSession(t, s)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_routine_start", Arguments: json.RawMessage(`{"slug":"hello","inputs":{"n":9007199254740993},"idempotency_key":"fixture-key","confirm_write":true,"wait_seconds":1}`)})
	if err != nil || result.IsError {
		t.Fatalf("%+v %v", result, err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if calls.Load() != 2 || !strings.Contains(text, `"terminal":true`) || !strings.Contains(text, "9007199254740993") || !strings.Contains(text, `"receipt"`) {
		t.Fatal(text)
	}
}

func TestMCPRoutineStartPolicyAndApprovalFailClosed(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	for _, tc := range []struct {
		allow, approval, confirm bool
		tags                     []string
	}{
		{false, false, true, nil}, {true, false, false, nil}, {true, false, true, []string{"crews"}}, {true, true, true, nil},
	} {
		s.allowWrite, s.requireApproval, s.writeTags = tc.allow, tc.approval, tc.tags
		session := testMCPSession(t, s)
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_routine_start", Arguments: map[string]any{"slug": "hello", "confirm_write": tc.confirm}})
		if err != nil || !result.IsError {
			t.Fatalf("policy accepted: %+v %v", result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized routine started")
	}
}

func TestMCPRoutineWaitTimeoutPreservesReceipt(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"run-1","status":"running"}`)
	})
	began := time.Now()
	value, err := s.waitRoutine(context.Background(), mcpWaitInput{RunID: "run-1", WaitSeconds: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["run_id"] != "run-1" || result["timed_out"] != true || result["resume_tool"] != "crewship_run_wait" || calls.Load() != 1 || time.Since(began) > 3*time.Second {
		t.Fatalf("lost timeout receipt: %+v", result)
	}
	for _, id := range []string{"..", "bad/id", "%2e%2e"} {
		if _, err := s.waitRoutine(context.Background(), mcpWaitInput{RunID: id, WaitSeconds: 1}, nil); err == nil {
			t.Errorf("unsafe run ID accepted: %s", id)
		}
	}
}

func TestMCPDeferredReceiptIsNotResubmittedOrPolled(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error("invented a poll target")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"pending_run_id":"private-1","status":"SCHEDULED"}`)
	})
	s.allowWrite = true
	session := testMCPSession(t, s)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_routine_start", Arguments: map[string]any{"slug": "hello", "confirm_write": true, "wait_seconds": 1}})
	if err != nil || result.IsError || calls.Load() != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `"wait_available":false`) {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestMCPRoutineWaitCancellationPreservesReceipt(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"run-1","status":"waiting"}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	value, err := s.waitRoutine(ctx, mcpWaitInput{RunID: "run-1"}, func(*cli.PipelineRunDetail) { cancel() })
	if err != nil {
		t.Fatal(err)
	}
	receipt := value.(map[string]any)
	if receipt["cancelled"] != true || receipt["terminal"] != false || receipt["run_id"] != "run-1" || calls.Load() != 1 {
		t.Fatalf("%+v", receipt)
	}
}

func TestMCPWriteOperationAllowlistIntersectsTagPolicy(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	start := "post_api_v1_workspaces_workspaceId_pipelines_slug_run"
	s.allowWrite = true
	s.writeOperations = []string{start}
	if err := s.validateWriteTags(); err != nil {
		t.Fatal(err)
	}
	op, err := s.operation(start)
	if err != nil || !s.writeAllowed(op) {
		t.Fatal(err)
	}
	s.writeTags = []string{"crews"}
	if s.writeAllowed(op) {
		t.Fatal("operation allowlist bypassed tags")
	}
	s.writeTags = nil
	other, _ := s.operation("post_api_v1_crews")
	if s.writeAllowed(other) {
		t.Fatal("allowed unrelated mutation")
	}
	admin, _ := s.operation("post_api_v1_admin_reap-orphan-containers")
	s.writeOperations = []string{admin.ID}
	if s.writeAllowed(admin) {
		t.Fatal("operation allowlist bypassed admin opt-in")
	}
	for _, ids := range [][]string{{"missing"}, {"get_api_v1_agents"}} {
		s.writeOperations = ids
		if s.validateWriteTags() == nil {
			t.Fatal("accepted invalid mutation allowlist")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("policy validation contacted API")
	}
}

func TestMCPRoutineWaitSendsRequestedProgress(t *testing.T) {
	s, _ := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"run-1","status":"running","output":"secret-output"}`)
	})
	progress := make(chan *mcp.ProgressNotificationParams, 4)
	session := testMCPSession(t, s, &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) { progress <- req.Params }})
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_run_wait", Arguments: map[string]any{"run_id": "run-1", "wait_seconds": 1}, Meta: mcp.Meta{"progressToken": "wait-fixture"}})
	if err != nil || result.IsError {
		t.Fatalf("%+v %v", result, err)
	}
	select {
	case update := <-progress:
		if update.ProgressToken != "wait-fixture" || update.Progress <= 0 || !strings.Contains(update.Message, "running") || strings.Contains(update.Message, "secret-output") {
			t.Fatalf("%+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("no progress notification")
	}
}

func TestMCPRestrictedStartUsesActorScopedStatus(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			fmt.Fprint(w, `{"run_id":"private-1","restricted":true,"status":"running","status_url":"https://attacker.invalid/steal"}`)
		} else {
			if !strings.HasSuffix(r.URL.Path, "/restricted-routine-runs/private-1") {
				t.Errorf("wrong authorization boundary: %s", r.URL.Path)
			}
			fmt.Fprint(w, `{"run_id":"private-1","status":"needs_reconciliation"}`)
		}
	})
	s.allowWrite = true
	session := testMCPSession(t, s)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_routine_start", Arguments: map[string]any{"slug": "hello", "confirm_write": true, "wait_seconds": 1}})
	if err != nil || result.IsError || calls.Load() != 2 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `"terminal":true`) {
		t.Fatalf("%+v %v", result, err)
	}
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_run_diagnose", Arguments: map[string]any{"run_id": "private-1", "restricted": true}})
	if err != nil || result.IsError || calls.Load() != 3 {
		t.Fatalf("queried private run public logs: %+v %v", result, err)
	}
}
