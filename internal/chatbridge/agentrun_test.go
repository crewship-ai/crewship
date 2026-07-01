package chatbridge

import "testing"

// TestChatInfoToAgentRunRequest_CarriesAllFields guards the single
// ChatInfo → AgentRunRequest converter. The two former construction
// sites (chat bridge + pipeline runner) had diverged: the pipeline copy
// silently dropped MemoryMB / CPUs / TTLHours / OpenedByUserID /
// RoleTitle, so pipeline-launched agents lost their resource limits and
// peer-card identity. This test asserts the converter carries the full
// union — both the info-sourced fields and the per-call overrides.
func TestChatInfoToAgentRunRequest_CarriesAllFields(t *testing.T) {
	info := ChatInfo{
		AgentID:        "agent-1",
		AgentSlug:      "eva",
		AgentRole:      "AGENT",
		CrewID:         "crew-1",
		CrewSlug:       "ops",
		WorkspaceID:    "ws-1",
		CLIAdapter:     "CLAUDE_CODE",
		SystemPrompt:   "you are eva",
		ToolProfile:    "CODING",
		TimeoutSecs:    111,
		MemoryEnabled:  true,
		NetworkMode:    "restricted",
		AllowedDomains: []string{"example.com"},
		// The five fields the pipeline copy used to drop:
		MemoryMB:       4096,
		CPUs:           2.5,
		TTLHours:       12,
		OpenedByUserID: "user-42",
		RoleTitle:      "Senior Engineer",
	}

	req := info.ToAgentRunRequest(AgentRunOverrides{
		ChatID:      "chat-1",
		ContainerID: "cid-1",
		UserMessage: "hello",
		LLMModel:    "claude-x",
		TimeoutSecs: 300,
		MemoryMB:    info.MemoryMB,
		CPUs:        info.CPUs,
		MaxTurns:    7,
	})

	// Per-call overrides.
	if req.ChatID != "chat-1" {
		t.Errorf("ChatID = %q, want chat-1", req.ChatID)
	}
	if req.ContainerID != "cid-1" {
		t.Errorf("ContainerID = %q, want cid-1", req.ContainerID)
	}
	if req.UserMessage != "hello" {
		t.Errorf("UserMessage = %q, want hello", req.UserMessage)
	}
	if req.LLMModel != "claude-x" {
		t.Errorf("LLMModel = %q, want claude-x", req.LLMModel)
	}
	if req.TimeoutSecs != 300 {
		t.Errorf("TimeoutSecs = %d, want 300", req.TimeoutSecs)
	}
	if req.MaxTurns != 7 {
		t.Errorf("MaxTurns = %d, want 7 (per-run --max-turns override)", req.MaxTurns)
	}

	// The previously-dropped resource + identity fields.
	if req.MemoryMB != 4096 {
		t.Errorf("MemoryMB = %d, want 4096", req.MemoryMB)
	}
	if req.CPUs != 2.5 {
		t.Errorf("CPUs = %v, want 2.5", req.CPUs)
	}
	if req.TTLHours != 12 {
		t.Errorf("TTLHours = %d, want 12", req.TTLHours)
	}
	if req.OpenedByUserID != "user-42" {
		t.Errorf("OpenedByUserID = %q, want user-42", req.OpenedByUserID)
	}
	if req.RoleTitle != "Senior Engineer" {
		t.Errorf("RoleTitle = %q, want Senior Engineer", req.RoleTitle)
	}

	// Spot-check identity fields flow straight from info.
	if req.AgentID != "agent-1" || req.CrewID != "crew-1" || req.WorkspaceID != "ws-1" {
		t.Errorf("identity fields not propagated: %+v", req)
	}
	if req.AgentSlug != "eva" || req.AgentRole != "AGENT" || req.CrewSlug != "ops" {
		t.Errorf("agent/crew fields not propagated: %+v", req)
	}
	if req.CLIAdapter != "CLAUDE_CODE" || req.SystemPrompt != "you are eva" || req.ToolProfile != "CODING" {
		t.Errorf("adapter/prompt/profile not propagated: %+v", req)
	}
	if !req.MemoryEnabled || req.NetworkMode != "restricted" || len(req.AllowedDomains) != 1 {
		t.Errorf("memory/network fields not propagated: %+v", req)
	}
}
