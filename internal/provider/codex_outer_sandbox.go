package provider

import (
	"context"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
)

// CodexOuterSandboxProvider proves the actual reserved runtime is suitable to
// replace Codex's nested sandbox. Missing capability must refuse opt-in.
type CodexOuterSandboxProvider interface {
	AttestCodexOuterSandbox(context.Context, string, string, managedlaunch.Descriptor) error
}

// CodexSandboxEvidence deliberately contains no prompt or credential fields.
type CodexSandboxEvidence struct {
	WorkspaceID    string `json:"workspace_id"`
	CrewID         string `json:"crew_id"`
	AgentID        string `json:"agent_id"`
	RunID          string `json:"run_id"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	Source         string `json:"source"`
	ImageID        string `json:"image_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	RevisionID     string `json:"revision_id"`
}
