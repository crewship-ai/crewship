package chatbridge

import "github.com/crewship-ai/crewship/internal/orchestrator"

// withoutMemoryForArtifacts: a turn whose message attached artifacts (its refs
// were recorded before the run, or the send was refused) runs without memory,
// so nothing it reads there becomes agent or crew memory. The server also
// refuses such a run's memory writes.
func withoutMemoryForArtifacts(req orchestrator.AgentRunRequest, hasArtifactRefs bool) orchestrator.AgentRunRequest {
	if !hasArtifactRefs {
		return req
	}
	return req.WithoutMemory()
}
