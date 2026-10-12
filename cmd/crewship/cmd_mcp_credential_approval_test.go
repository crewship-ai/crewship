package main

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPApprovalRefusesCredentialRotation(t *testing.T) {
	for _, tool := range []string{"crewship_write", "crewship_routine_start"} {
		t.Run(tool, func(t *testing.T) {
			s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"run_id":"run-1"}`)
			})
			s.allowWrite, s.requireApproval = true, true
			var token atomic.Value
			token.Store("before-login-change")
			s.refreshClient = func() (*cli.Client, error) { c := *s.client; c.Token = token.Load().(string); return &c, nil }
			session := testMCPSession(t, s, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				token.Store("after-login-change")
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
			}})
			args := map[string]any{"confirm_write": true}
			if tool == "crewship_write" {
				args["operation_id"] = "post_api_v1_crews"
			} else {
				args["slug"] = "hello"
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil || !result.IsError || calls.Load() != 0 {
				t.Fatalf("approval crossed credential rotation: error=%v result=%+v API calls=%d", err, result, calls.Load())
			}
		})
	}
}

func TestMCPApprovalExecutesWithTheApprovedCredentialSnapshot(t *testing.T) {
	s, calls := testCLIMCP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer approved-credential" {
			t.Error("execution reloaded an unapproved credential")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	})
	s.allowWrite, s.requireApproval = true, true
	var reads atomic.Int64
	s.refreshClient = func() (*cli.Client, error) {
		c := *s.client
		c.Token = "approved-credential"
		if reads.Add(1) > 2 {
			c.Token = "subsequent-login"
		}
		return &c, nil
	}
	session := testMCPSession(t, s, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approve": true}}, nil
	}})
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "crewship_write", Arguments: map[string]any{"operation_id": "post_api_v1_crews", "confirm_write": true}})
	if err != nil || result.IsError || calls.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("snapshot was not retained: error=%v API calls=%d credential reads=%d", err, calls.Load(), reads.Load())
	}
}
