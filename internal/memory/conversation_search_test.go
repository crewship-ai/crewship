package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// stubConvSearcher records the agent_id/query/limit it was called with and
// returns canned hits so the dispatcher wiring can be asserted in isolation
// (no DB).
type stubConvSearcher struct {
	gotAgentID string
	gotQuery   string
	gotLimit   int
	hits       []ConvSearchHit
	err        error
}

func (s *stubConvSearcher) Search(_ context.Context, agentID, query string, limit int) ([]ConvSearchHit, error) {
	s.gotAgentID = agentID
	s.gotQuery = query
	s.gotLimit = limit
	return s.hits, s.err
}

// TestConversationSearch_SchemaRegistered locks the tool's presence and that
// its schema requires `q`.
func TestConversationSearch_SchemaRegistered(t *testing.T) {
	s, ok := ToolSchemas()["conversation.search"]
	if !ok {
		t.Fatal("conversation.search not registered")
	}
	var raw struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(s.InputSchema, &raw); err != nil {
		t.Fatalf("schema not JSON: %v", err)
	}
	found := false
	for _, r := range raw.Required {
		if r == "q" {
			found = true
		}
	}
	if !found {
		t.Errorf("conversation.search schema must require 'q'; required=%v", raw.Required)
	}
}

// TestConversationSearch_DispatchHappyPath verifies the dispatcher forwards
// the AgentContext's agent_id (NOT a model-supplied one), the query, and a
// clamped limit, and renders the hits envelope.
func TestConversationSearch_DispatchHappyPath(t *testing.T) {
	stub := &stubConvSearcher{
		hits: []ConvSearchHit{
			{ID: "m1", SessionID: "s1", Role: "user", Content: "deploy staging", Timestamp: "2026-06-01T00:00:00.000Z"},
		},
	}
	d := NewDispatcher(AgentContext{AgentID: "agentA"}, WithConvSearcher(stub))

	res, err := d.Dispatch(context.Background(), ToolCall{
		Name: "conversation.search",
		Args: json.RawMessage(`{"q":"deploy","limit":5}`),
	})
	if err != nil {
		t.Fatalf("dispatch err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected IsError: %s", res.Content)
	}
	if stub.gotAgentID != "agentA" {
		t.Errorf("agent_id forwarded = %q, want agentA", stub.gotAgentID)
	}
	if stub.gotQuery != "deploy" || stub.gotLimit != 5 {
		t.Errorf("forwarded query=%q limit=%d", stub.gotQuery, stub.gotLimit)
	}
	if !strings.Contains(res.Content, "deploy staging") || !strings.Contains(res.Content, `"count": 1`) {
		t.Errorf("envelope missing hit/count: %s", res.Content)
	}
}

// TestConversationSearch_AgentIDFromContext proves the model cannot spoof a
// different agent_id — the args have no such field, and the dispatcher uses
// only AgentContext.AgentID.
func TestConversationSearch_AgentIDFromContext(t *testing.T) {
	stub := &stubConvSearcher{}
	d := NewDispatcher(AgentContext{AgentID: "trusted-agent"}, WithConvSearcher(stub))
	// Even if the model stuffs an agent_id into args, additionalProperties
	// is false in the schema and the handler ignores it regardless.
	_, _ = d.Dispatch(context.Background(), ToolCall{
		Name: "conversation.search",
		Args: json.RawMessage(`{"q":"x","agent_id":"victim-agent"}`),
	})
	if stub.gotAgentID != "trusted-agent" {
		t.Errorf("agent_id = %q, want trusted-agent (model must not override)", stub.gotAgentID)
	}
}

func TestConversationSearch_DispatchErrors(t *testing.T) {
	cases := []struct {
		name      string
		ac        AgentContext
		searcher  ConvSearcher
		args      string
		wantInMsg string
	}{
		{
			name:      "no_searcher_wired",
			ac:        AgentContext{AgentID: "a"},
			searcher:  nil,
			args:      `{"q":"hi"}`,
			wantInMsg: "not available",
		},
		{
			name:      "empty_query",
			ac:        AgentContext{AgentID: "a"},
			searcher:  &stubConvSearcher{},
			args:      `{"q":"   "}`,
			wantInMsg: "q is required",
		},
		{
			name:      "missing_agent_identity",
			ac:        AgentContext{AgentID: ""},
			searcher:  &stubConvSearcher{},
			args:      `{"q":"hi"}`,
			wantInMsg: "agent identity unavailable",
		},
		{
			name:      "invalid_json",
			ac:        AgentContext{AgentID: "a"},
			searcher:  &stubConvSearcher{},
			args:      `{"q":`,
			wantInMsg: "invalid args",
		},
		{
			name:      "backend_error",
			ac:        AgentContext{AgentID: "a"},
			searcher:  &stubConvSearcher{err: errors.New("db down")},
			args:      `{"q":"hi"}`,
			wantInMsg: "db down",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opts []DispatcherOption
			if tc.searcher != nil {
				opts = append(opts, WithConvSearcher(tc.searcher))
			}
			d := NewDispatcher(tc.ac, opts...)
			res, err := d.Dispatch(context.Background(), ToolCall{
				Name: "conversation.search",
				Args: json.RawMessage(tc.args),
			})
			if err != nil {
				t.Fatalf("dispatch returned hard error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected IsError, got content: %s", res.Content)
			}
			if !strings.Contains(res.Content, tc.wantInMsg) {
				t.Errorf("msg %q does not contain %q", res.Content, tc.wantInMsg)
			}
		})
	}
}

// TestConversationSearch_LimitClamp verifies out-of-range limits clamp to the
// default rather than erroring.
func TestConversationSearch_LimitClamp(t *testing.T) {
	for _, lim := range []int{-1, 0, 9999} {
		stub := &stubConvSearcher{}
		d := NewDispatcher(AgentContext{AgentID: "a"}, WithConvSearcher(stub))
		args, _ := json.Marshal(map[string]any{"q": "hi", "limit": lim})
		res, _ := d.Dispatch(context.Background(), ToolCall{Name: "conversation.search", Args: args})
		if res.IsError {
			t.Fatalf("limit %d errored: %s", lim, res.Content)
		}
		if stub.gotLimit != 20 {
			t.Errorf("limit %d clamped to %d, want 20", lim, stub.gotLimit)
		}
	}
}

// TestConversationSearch_CancelledContext returns a recoverable error.
func TestConversationSearch_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := NewDispatcher(AgentContext{AgentID: "a"}, WithConvSearcher(&stubConvSearcher{}))
	res, _ := d.Dispatch(ctx, ToolCall{Name: "conversation.search", Args: json.RawMessage(`{"q":"hi"}`)})
	if !res.IsError || !strings.Contains(res.Content, "cancelled") {
		t.Errorf("expected cancelled IsError, got %+v", res)
	}
}
