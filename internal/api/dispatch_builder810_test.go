package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// fakeAgentResolver returns a canned ChatInfo, standing in for the in-process
// IPCResolver so the dispatch builders can be exercised without a live server.
type fakeAgentResolver struct {
	info *chatbridge.ChatInfo
	err  error

	gotAgentID     string
	gotWorkspaceID string
}

func (f *fakeAgentResolver) ResolveAgent(_ context.Context, agentID, workspaceID string) (*chatbridge.ChatInfo, error) {
	f.gotAgentID = agentID
	f.gotWorkspaceID = workspaceID
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func fullResolvedChatInfo() *chatbridge.ChatInfo {
	return &chatbridge.ChatInfo{
		AgentID:      "agent-1",
		AgentSlug:    "eva",
		AgentRole:    "AGENT",
		CrewID:       "crew-1",
		CrewSlug:     "ops",
		WorkspaceID:  "ws-1",
		CLIAdapter:   "CLAUDE_CODE",
		SystemPrompt: "you are eva\n\n[SKILLS AVAILABLE]\npdf-fill\n[END SKILLS AVAILABLE]",
		ToolProfile:  "CODING",
		TimeoutSecs:  120,
		MemoryMB:     2048,
		CPUs:         1.5,
		MCPServers: []orchestrator.MCPServerConfig{
			{ID: "mcp-1", Name: "linear", Transport: "http"},
		},
		InstalledSkills: []orchestrator.SkillBundle{
			{Slug: "pdf-fill", Content: "fill pdfs"},
		},
		PreferredLanguage: "Czech",
		ApprovalMode:      "sync",
	}
}

// TestBuildAssignmentRunRequest_ThroughBuilder proves the mission /
// sidecar-assign path now carries the assembled prompt, MCP servers, skills,
// the crew-policy ApprovalMode, and creator attribution — the fields it
// dropped when it read raw system_prompt_legacy (#810).
func TestBuildAssignmentRunRequest_ThroughBuilder(t *testing.T) {
	fr := &fakeAgentResolver{info: fullResolvedChatInfo()}
	h := &AssignmentHandler{logger: testLogger(), resolver: fr}

	body := createAssignmentBody{
		Task:            "ship it",
		CrewID:          "crew-1",
		WorkspaceID:     "ws-1",
		ChatID:          "chat-1",
		MissionID:       "mission-1",
		AuthorAgentID:   "agent-boss",
		CreatedByUserID: "user-9",
	}
	target := targetAgentInfo{ID: "agent-1", Slug: "eva", CrewSlug: "ops"}

	req, err := h.buildAssignmentRunRequest(context.Background(), body, target, "cid-1", "AGENT", true)
	if err != nil {
		t.Fatalf("builder returned error: %v", err)
	}

	if fr.gotAgentID != "agent-1" || fr.gotWorkspaceID != "ws-1" {
		t.Errorf("resolver called with (%q,%q), want (agent-1,ws-1)", fr.gotAgentID, fr.gotWorkspaceID)
	}
	if !strings.Contains(req.SystemPrompt, "[SKILLS AVAILABLE]") {
		t.Errorf("SystemPrompt is not the assembled prompt: %q", req.SystemPrompt)
	}
	if len(req.MCPServers) != 1 {
		t.Errorf("MCPServers dropped: %+v", req.MCPServers)
	}
	if len(req.Skills) != 1 {
		t.Errorf("Skills dropped: %+v", req.Skills)
	}
	if req.ApprovalMode != "sync" {
		t.Errorf("ApprovalMode = %q, want sync (HITL gate revived)", req.ApprovalMode)
	}
	if req.AuthorAgentID != "agent-boss" || req.CreatedByUserID != "user-9" {
		t.Errorf("attribution not threaded: author=%q user=%q", req.AuthorAgentID, req.CreatedByUserID)
	}
	if req.MissionID != "mission-1" {
		t.Errorf("MissionID = %q, want mission-1", req.MissionID)
	}
	// Dispatch-specific overrides.
	if req.AgentRole != "AGENT" || !req.SkipSidecar || !req.SkipConvHistory {
		t.Errorf("dispatch flags wrong: role=%q skipSidecar=%v skipConv=%v", req.AgentRole, req.SkipSidecar, req.SkipConvHistory)
	}
	if req.ContainerID != "cid-1" || req.UserMessage != "ship it" {
		t.Errorf("container/message not set: cid=%q msg=%q", req.ContainerID, req.UserMessage)
	}
}

// TestBuildPeerQueryRequest_ThroughBuilder proves the peer-query path carries
// the assembled prompt + MCP + skills + ApprovalMode, with the [PEER QUERY]
// block prepended (#810).
func TestBuildPeerQueryRequest_ThroughBuilder(t *testing.T) {
	fr := &fakeAgentResolver{info: fullResolvedChatInfo()}
	h := &QueryHandler{logger: testLogger(), resolver: fr}

	body := createQueryBody{
		TargetSlug:  "eva",
		Question:    "what is the deploy cmd?",
		FromSlug:    "sam",
		CrewID:      "crew-1",
		WorkspaceID: "ws-1",
		ChatID:      "chat-1",
	}
	target := targetAgentInfo{ID: "agent-1", Slug: "eva", CrewSlug: "ops"}

	peerBlock := "[PEER QUERY from @sam]\nAnswer concisely."
	req, err := h.buildPeerQueryRequest(context.Background(), body, target, "cid-1", peerBlock)
	if err != nil {
		t.Fatalf("builder returned error: %v", err)
	}

	if !strings.Contains(req.SystemPrompt, "[SKILLS AVAILABLE]") {
		t.Errorf("SystemPrompt lost the assembled prompt: %q", req.SystemPrompt)
	}
	if !strings.Contains(req.SystemPrompt, "[PEER QUERY from @sam]") {
		t.Errorf("SystemPrompt missing prepended peer block: %q", req.SystemPrompt)
	}
	if len(req.MCPServers) != 1 || len(req.Skills) != 1 {
		t.Errorf("MCP/skills dropped: mcp=%+v skills=%+v", req.MCPServers, req.Skills)
	}
	if req.ApprovalMode != "sync" {
		t.Errorf("ApprovalMode = %q, want sync (HITL gate revived)", req.ApprovalMode)
	}
	if !req.SkipSidecar || !req.SkipConvHistory || req.AgentRole != "AGENT" {
		t.Errorf("peer flags wrong: role=%q skipSidecar=%v skipConv=%v", req.AgentRole, req.SkipSidecar, req.SkipConvHistory)
	}
	if req.UserMessage != body.Question {
		t.Errorf("UserMessage = %q, want %q", req.UserMessage, body.Question)
	}
}

// TestBuildAssignmentRunRequest_NoResolverFailsClosed: with no resolver wired,
// the builder returns an ERROR rather than a legacy raw build — the single
// builder is the only dispatch path (there is no MCP-blind fallback).
func TestBuildAssignmentRunRequest_NoResolverFailsClosed(t *testing.T) {
	h := &AssignmentHandler{logger: testLogger()} // resolver nil
	body := createAssignmentBody{Task: "t", CrewID: "c", ChatID: "chat-1", AuthorAgentID: "a"}
	target := targetAgentInfo{ID: "agent-1", Slug: "eva", SystemPrompt: "legacy", CLIAdapter: "CLAUDE_CODE"}

	if _, err := h.buildAssignmentRunRequest(context.Background(), body, target, "cid-1", "AGENT", true); err == nil {
		t.Fatal("expected an error with no resolver wired (fail closed), got nil")
	}
}

// TestBuildAssignmentRunRequest_ResolverErrorFailsClosed: a resolve failure must
// NOT silently fall back to a legacy build. It returns an error so the
// assignment fails loudly instead of dispatching an MCP-blind, HITL-inert,
// unassembled-prompt run (the silent-degrade regression this PR removes).
func TestBuildAssignmentRunRequest_ResolverErrorFailsClosed(t *testing.T) {
	fr := &fakeAgentResolver{err: fmt.Errorf("resolve boom")}
	h := &AssignmentHandler{logger: testLogger(), resolver: fr}
	body := createAssignmentBody{Task: "t", CrewID: "c", WorkspaceID: "ws-1", ChatID: "chat-1"}
	target := targetAgentInfo{ID: "agent-1", Slug: "eva"}

	if _, err := h.buildAssignmentRunRequest(context.Background(), body, target, "cid-1", "AGENT", true); err == nil {
		t.Fatal("resolver error must fail closed (no legacy degrade), got nil error")
	}
}

// TestBuildPeerQueryRequest_ResolverErrorFailsClosed mirrors the assignment
// case for the peer-query path.
func TestBuildPeerQueryRequest_ResolverErrorFailsClosed(t *testing.T) {
	fr := &fakeAgentResolver{err: fmt.Errorf("resolve boom")}
	h := &QueryHandler{logger: testLogger(), resolver: fr}
	body := createQueryBody{TargetSlug: "eva", Question: "q", FromSlug: "sam", CrewID: "c", WorkspaceID: "ws-1", ChatID: "chat-1"}
	target := targetAgentInfo{ID: "agent-1", Slug: "eva"}

	if _, err := h.buildPeerQueryRequest(context.Background(), body, target, "cid-1", "[PEER QUERY]"); err == nil {
		t.Fatal("resolver error must fail closed (no legacy degrade), got nil error")
	}
}
