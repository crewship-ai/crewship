package chatbridge

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestATurnWithArtifactsRunsWithoutMemory(t *testing.T) {
	req := orchestrator.AgentRunRequest{MemoryEnabled: true}
	if withoutMemoryForArtifacts(req, true).MemoryEnabled {
		t.Fatal("a turn with attached artifacts reads memory")
	}
	if !withoutMemoryForArtifacts(req, false).MemoryEnabled {
		t.Fatal("an ordinary turn lost its memory")
	}
}
