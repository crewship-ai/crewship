package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAISearchIntentRanking(t *testing.T) {
	doc, err := loadAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, method, path string }{
		{"list agents", "GET", "/api/v1/agents"},
		{"list crews", "GET", "/api/v1/crews"},
		{"list routines", "GET", "/api/v1/workspaces/{workspaceId}/pipelines"},
		{"create mission", "POST", "/api/v1/crews/{crewId}/missions"},
		{"list runs", "GET", "/api/v1/runs"},
		{"create crew", "POST", "/api/v1/crews"},
		{"start routine", "POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/run"},
		{"launch pipeline", "POST", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/run"},
		{"new crew", "POST", "/api/v1/crews"},
		{"search conversations", "POST", "/api/v1/conversations/search"},
		{"get user avatar", "GET", "/api/v1/users/{id}/avatar"},
		{"create chat share", "POST", "/api/v1/agents/{agentId}/chats/{chatId}/shares"},
		{"server health", "GET", "/api/health"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			ops, err := doc.operations(tc.query, "")
			if err != nil || len(ops) == 0 {
				t.Fatalf("empty search: %v", err)
			}
			if ops[0].Path != tc.path || ops[0].Method != tc.method {
				t.Fatalf("top result %+v; want %s %s", ops[0], tc.method, tc.path)
			}
		})
	}
	if score := apiSearchScore(apiOperation{Path: "/api/v1/runtime", Summary: "Get runtime"}, "run"); score >= 0 {
		t.Fatal("run matched runtime")
	}
	ops, _ := doc.operations("", "")
	for _, op := range ops {
		if op.Summary == "" || op.Description == "" {
			t.Errorf("missing prose: %s", op.ID)
		}
		if strings.Join(strings.Fields(op.Summary), " ") != op.Summary {
			t.Errorf("unclean summary: %s: %q", op.ID, op.Summary)
		}
		for _, internal := range []string{"Inspect the parameters and request/response", "CrewFileSave", "Put is PUT"} {
			if strings.Contains(op.Description, internal) {
				t.Errorf("non-public prose: %s: %q", op.ID, internal)
			}
		}
	}
}

func TestMCPReadWriteAndWorkspaceBoundary(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	session := testMCPSession(t, s)
	readPost := mcpOpID(t, s, "POST", "/api/v1/conversations/search")
	mutation := mcpOpID(t, s, "POST", "/api/v1/agents")
	for _, tc := range []struct {
		tool, id string
		fail     bool
	}{
		{"crewship_read", readPost, false}, {"crewship_read", mutation, true}, {"crewship_write", readPost, true}, {"crewship_write", mutation, true},
	} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.tool, Arguments: map[string]any{"operation_id": tc.id, "confirm_write": true}})
		if err != nil || result.IsError != tc.fail {
			t.Fatalf("%+v: %+v %v", tc, result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected HTTP calls: %d", calls.Load())
	}
	tools, _ := session.ListTools(context.Background(), nil)
	for _, tool := range tools.Tools {
		if tool.Name == "crewship_read" && !tool.Annotations.ReadOnlyHint {
			t.Fatal("read annotation missing")
		}
		if tool.Name == "crewship_write" && (tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Fatal("write annotation misleading")
		}
	}
	for _, op := range s.operations {
		if !strings.Contains(op.Path, "{workspaceId}") {
			continue
		}
		_, err := s.request(context.Background(), mcpRequestInput{OperationID: op.ID, DryRun: true, PathParams: map[string]string{"workspaceId": "other"}})
		if err == nil || !strings.Contains(err.Error(), "workspaceId cannot override") {
			t.Fatalf("workspace bypass %s: %v", op.ID, err)
		}
	}
	s.allowWrite = true
	admin := apiOperation{Path: "/api/v1/admin/reap-orphan-containers", Tags: []string{"admin"}}
	if s.writeAllowed(admin) {
		t.Fatal("admin enabled implicitly")
	}
	s.writeTags = []string{"agents"}
	if !s.writeAllowed(apiOperation{Tags: []string{"agents"}}) || s.writeAllowed(apiOperation{Tags: []string{"crews"}}) || s.writeAllowed(admin) {
		t.Fatal("tag boundary")
	}
	s.writeTags = []string{"admin"}
	if !s.writeAllowed(admin) {
		t.Fatal("explicit admin denied")
	}
	s.writeTags = []string{"typo"}
	if s.validateWriteTags() == nil {
		t.Fatal("unknown write tag accepted")
	}
}

func TestMCPApprovalUnavailableFailsClosed(t *testing.T) {
	s, calls := testCLIMCP(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	s.allowWrite = true
	s.requireApproval = true
	session := testMCPSession(t, s)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_write", Arguments: map[string]any{"operation_id": mcpOpID(t, s, "POST", "/api/v1/agents"), "confirm_write": true}})
	if err != nil || !result.IsError || calls.Load() != 0 {
		t.Fatalf("approval bypass: %+v %v calls=%d", result, err, calls.Load())
	}
}

func TestMCPHumanApprovalDecision(t *testing.T) {
	for _, protocol := range []string{"2025-06-18", "2025-11-25", "2026-07-28"} {
		for _, accept := range []bool{false, true} {
			t.Run(protocol+"/"+map[bool]string{true: "accept", false: "decline"}[accept], func(t *testing.T) {
				s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body["number"]) != "9007199254740993" {
						t.Errorf("approval changed JSON number: %v %s", err, body["number"])
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{}`))
				})
				s.allowWrite = true
				s.requireApproval = true
				server, err := s.server()
				if err != nil {
					t.Fatal(err)
				}
				a, b := mcp.NewInMemoryTransports()
				ctx := context.Background()
				ss, err := server.Connect(ctx, a, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer ss.Close()
				prompted := false
				client := mcp.NewClient(&mcp.Implementation{Name: "approval-test", Version: "1"}, &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
					prompted = true
					if !strings.Contains(req.Params.Message, "post_api_v1_agents") {
						t.Error("approval does not identify operation")
					}
					action := "decline"
					if accept {
						action = "accept"
					}
					return &mcp.ElicitResult{Action: action, Content: map[string]any{"approve": accept}}, nil
				}})
				session, err := client.Connect(ctx, b, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "crewship_write", Arguments: map[string]any{"operation_id": mcpOpID(t, s, "POST", "/api/v1/agents"), "confirm_write": true, "body": json.RawMessage(`{"number":9007199254740993}`)}})
				if err != nil || result.IsError == accept || !prompted {
					t.Fatalf("decision: result=%+v err=%v prompted=%v", result, err, prompted)
				}
				want := int64(0)
				if accept {
					want = 1
				}
				if calls.Load() != want {
					t.Fatalf("calls=%d want=%d", calls.Load(), want)
				}
			})
		}
	}
}

func TestMCPWorkspacePathInjected(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		want := "/api/v1/workspaces/cworkspace012345678901234/pipelines/demo"
		if r.URL.Path != want || r.URL.Query().Get("workspace_id") != "cworkspace012345678901234" {
			t.Errorf("workspace mismatch: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	id := mcpOpID(t, s, "GET", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}")
	_, err := s.request(context.Background(), mcpRequestInput{OperationID: id, PathParams: map[string]string{"slug": "demo"}})
	if err != nil || calls.Load() != 1 {
		t.Fatalf("injected request %v calls=%d", err, calls.Load())
	}
	s.client.WorkspaceID = ""
	_, err = s.request(context.Background(), mcpRequestInput{OperationID: id, PathParams: map[string]string{"slug": "demo"}})
	if err == nil || calls.Load() != 1 {
		t.Fatal("unconfigured workspace accepted")
	}
}

func TestMCPApprovalContinuationBound(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	a, b := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) { return nil, nil }})
	cs, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	original := mcpRequestInput{OperationID: "create", ConfirmWrite: true, Body: json.RawMessage(`{"x":1}`)}
	accepted := mcp.InputResponseMap{"approve": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}}
	for _, scenario := range []string{"changed body", "forged token", "expired", "single use"} {
		t.Run(scenario, func(t *testing.T) {
			gate := &mcpApprovalGate{}
			req := &mcp.CallToolRequest{Session: ss, Params: &mcp.CallToolParamsRaw{}}
			pending, err := gate.approve(req, original)
			if err != nil {
				t.Fatal(err)
			}
			req.Params.RequestState = pending.RequestState
			req.Params.InputResponses = accepted
			next := original
			switch scenario {
			case "changed body":
				next.Body = json.RawMessage(`{"x":2}`)
			case "forged token":
				req.Params.RequestState = "forged"
			case "expired":
				stored := gate.pending[pending.RequestState]
				stored.expires = time.Now().Add(-time.Second)
				gate.pending[pending.RequestState] = stored
			case "single use":
				if _, err := gate.approve(req, next); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := gate.approve(req, next); err == nil {
				t.Fatal("approval continuation bypass")
			}
		})
	}
}

func TestMCPWorkspaceResolutionFailureDoesNotReachTarget(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces" {
			t.Error("target reached after failed workspace resolution")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	s.client.WorkspaceID = "unresolved-team"
	id := mcpOpID(t, s, "GET", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}")
	_, err := s.request(context.Background(), mcpRequestInput{OperationID: id, PathParams: map[string]string{"slug": "demo"}})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("resolution must fail closed: %v, calls=%d", err, calls.Load())
	}
}

func TestMCPWorkspacePreflightRefusesRedirect(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces" {
			t.Error("workspace preflight followed a redirect")
		}
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(http.StatusFound)
	})
	s.client.WorkspaceID = "mcp-preflight-redirect"
	id := mcpOpID(t, s, "GET", "/api/v1/workspaces/{workspaceId}/pipelines/{slug}")
	_, err := s.request(context.Background(), mcpRequestInput{OperationID: id, PathParams: map[string]string{"slug": "demo"}})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("redirected preflight: err=%v calls=%d", err, calls.Load())
	}
}
