package pipeline

import "context"

// ArtifactPublisher captures explicit deliverables; file reads/tool logs are not outputs.
type ArtifactPublisher interface {
	PublishRunArtifacts(ctx context.Context, workspaceID, crewID, runID, executionID, output, state string) error
}
type ArtifactPublishFunc func(ctx context.Context, workspaceID, crewID, runID, executionID, output, state string) error

func (f ArtifactPublishFunc) PublishRunArtifacts(ctx context.Context, workspaceID, crewID, runID, executionID, output, state string) error {
	return f(ctx, workspaceID, crewID, runID, executionID, output, state)
}
func (r *OrchestratorRunner) PublishRunArtifacts(ctx context.Context, workspaceID, crewID, runID, executionID, output, state string) error {
	if r.artifactPublisher == nil {
		return nil
	}
	return r.artifactPublisher.PublishRunArtifacts(ctx, workspaceID, crewID, runID, executionID, output, state)
}
