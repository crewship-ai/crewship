package chatbridge

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// TestEveryDispatchPathCarriesMCPAndSkills guards the single request-builder
// (#810). Every agent-dispatch path — chat, pipeline, cron/scheduler,
// webhook, mission, peer — funnels through ChatInfo.ToAgentRunRequest, so
// asserting the builder carries the MCP/skills/persona group + the revived
// HITL ApprovalMode + creator attribution proves the invariant holds for
// all of them structurally, instead of per-path.
//
// RED on main: ChatInfo has no ApprovalMode, AgentRunOverrides has no
// creator attribution, and ToAgentRunRequest sets neither — so mission,
// peer and cron dispatched agents ran tool-blind and the harbormaster gate
// was dead (ApprovalMode always "").
func TestEveryDispatchPathCarriesMCPAndSkills(t *testing.T) {
	info := ChatInfo{
		AgentID:     "agent-1",
		AgentSlug:   "eva",
		AgentRole:   "AGENT",
		CrewID:      "crew-1",
		CrewSlug:    "ops",
		WorkspaceID: "ws-1",
		CLIAdapter:  "CLAUDE_CODE",
		// The assembled system prompt (NOT the raw system_prompt_legacy):
		// carries [SKILLS AVAILABLE], persona, ethos, connected integrations.
		SystemPrompt: "you are eva\n\n[SKILLS AVAILABLE]\npdf-fill\n[END SKILLS AVAILABLE]",
		ToolProfile:  "CODING",
		// Structured MCP + skills for sidecar injection / on-disk discovery.
		MCPServers: []orchestrator.MCPServerConfig{
			{ID: "mcp-1", Name: "linear", Transport: "http", Endpoint: "https://mcp.linear.app"},
		},
		InstalledSkills: []orchestrator.SkillBundle{
			{Slug: "pdf-fill", Content: "---\nname: pdf-fill\n---\nfill pdfs"},
		},
		PreferredLanguage: "Czech",
		// The HITL policy the builder must stamp onto the request so the
		// harbormaster gate actually fires (was never set → ModeNone).
		ApprovalMode: "sync",
	}

	req := info.ToAgentRunRequest(AgentRunOverrides{
		ChatID:      "chat-1",
		ContainerID: "cid-1",
		UserMessage: "do the thing",
		// Creator attribution [4]: the reporter (human or authoring agent),
		// threaded mission → run so the journal shows WHO asked, not just
		// the executor.
		CreatedByUserID: "user-9",
		AuthorAgentID:   "agent-boss",
	})

	if len(req.MCPServers) != 1 || req.MCPServers[0].Name != "linear" {
		t.Errorf("MCPServers not carried: %+v", req.MCPServers)
	}
	if len(req.Skills) != 1 || req.Skills[0].Slug != "pdf-fill" {
		t.Errorf("Skills not carried: %+v", req.Skills)
	}
	if req.SystemPrompt != info.SystemPrompt {
		t.Errorf("SystemPrompt must be the assembled prompt, got %q", req.SystemPrompt)
	}
	if req.PreferredLanguage != "Czech" {
		t.Errorf("PreferredLanguage = %q, want Czech", req.PreferredLanguage)
	}
	if req.ApprovalMode != "sync" {
		t.Errorf("ApprovalMode = %q, want sync (revives the harbormaster HITL gate)", req.ApprovalMode)
	}
	if req.CreatedByUserID != "user-9" {
		t.Errorf("CreatedByUserID = %q, want user-9", req.CreatedByUserID)
	}
	if req.AuthorAgentID != "agent-boss" {
		t.Errorf("AuthorAgentID = %q, want agent-boss", req.AuthorAgentID)
	}
}
