//go:build windows

package docker

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// Agent CLI staging is unix-only; the agent-cli attestation cases are
// compiled out on Windows rather than skipped at run time.
func stageTestAgentCLI(*testing.T) *runtimestage.AgentCLIArtifact { return nil }
