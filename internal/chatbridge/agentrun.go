package chatbridge

import "github.com/crewship-ai/crewship/internal/orchestrator"

// AgentRunOverrides carries the per-call values that differ between the
// two construction sites of orchestrator.AgentRunRequest — the chat
// bridge (Bridge.handleMessage) and the pipeline runner
// (OrchestratorRunner.RunStep). Every other field of the request is
// derived verbatim from the resolved ChatInfo.
//
// These values can't come from ChatInfo because each caller resolves
// them differently:
//
//   - ChatID/ContainerID: the bridge uses the live chat + cached
//     container; the pipeline mints a synthetic chat and ensures its
//     own runtime.
//   - UserMessage: chat content vs. the rendered pipeline step prompt.
//   - LLMModel: agent default vs. a step's tier override (req.Model).
//   - TimeoutSecs: agent default vs. the tighter of agent/step plus a
//     pipeline fallback.
//   - MemoryMB/CPUs: the bridge applies the server's configured
//     defaults; the pipeline passes the agent's own configured limits.
type AgentRunOverrides struct {
	ChatID      string
	ContainerID string
	UserMessage string
	LLMModel    string
	TimeoutSecs int
	MemoryMB    int
	CPUs        float64
	// MaxTurns is a per-run cap on the adapter agent loop, sourced from the
	// caller (e.g. the `--max-turns` CLI flag threaded through the WebSocket
	// message). 0 leaves orchestrator.AgentRunRequest.MaxTurns unset so the
	// adapter falls back to its interactive default.
	MaxTurns int
}

// ToAgentRunRequest maps a resolved ChatInfo plus the per-call overrides
// into a fully-populated orchestrator.AgentRunRequest.
//
// This is the SINGLE source of truth for that mapping. It previously
// existed as two hand-written ~25-field struct literals (here and in the
// pipeline runner) that had silently diverged: the pipeline copy dropped
// MemoryMB / CPUs / TTLHours / OpenedByUserID / RoleTitle, so
// pipeline-launched agents lost their resource limits and peer-card
// identity. Funnel both callers through this method so adding a field to
// AgentRunRequest can't leave one path behind again.
func (info ChatInfo) ToAgentRunRequest(o AgentRunOverrides) orchestrator.AgentRunRequest {
	return orchestrator.AgentRunRequest{
		AgentID:            info.AgentID,
		AgentSlug:          info.AgentSlug,
		AgentRole:          info.AgentRole,
		CrewID:             info.CrewID,
		CrewSlug:           info.CrewSlug,
		WorkspaceID:        info.WorkspaceID,
		ChatID:             o.ChatID,
		ContainerID:        o.ContainerID,
		CLIAdapter:         info.CLIAdapter,
		LLMModel:           o.LLMModel,
		SystemPrompt:       info.SystemPrompt,
		UserMessage:        o.UserMessage,
		ToolProfile:        info.ToolProfile,
		Credentials:        info.Credentials,
		TimeoutSecs:        o.TimeoutSecs,
		MaxTurns:           o.MaxTurns,
		MemoryEnabled:      info.MemoryEnabled,
		CrewMembers:        info.CrewMembers,
		NetworkMode:        info.NetworkMode,
		AllowedDomains:     info.AllowedDomains,
		MemoryMB:           o.MemoryMB,
		CPUs:               o.CPUs,
		TTLHours:           info.TTLHours,
		MCPServers:         info.MCPServers,
		CrewMCPConfigJSON:  info.CrewMCPConfigJSON,
		AgentMCPConfigJSON: info.AgentMCPConfigJSON,
		PreferredLanguage:  info.PreferredLanguage,
		Skills:             info.InstalledSkills,
		OpenedByUserID:     info.OpenedByUserID,
		RoleTitle:          info.RoleTitle,
	}
}
