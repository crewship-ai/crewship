package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

// --artifact-target carries the same explicit human purpose the artifact
// viewer sends with its chat message (hooks/use-artifact-agent-dispatch.ts):
// {submission_id, page_id, comment_id, base_version, kind?} for a queued
// comment task, or {submission_id} alone for a "Describe it" creation. The
// host binds it to the stored message and starts the task only on that send
// (internal/api/pages_artifact_chat.go); the CLI never constructs authority.

const runArtifactTargetMaxBytes = 2048

func readRunArtifactTarget(path string) (*artifacts.ProjectIntent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, runArtifactTargetMaxBytes+1))
	if err != nil || len(raw) > runArtifactTargetMaxBytes {
		return nil, cli.WithExitCode(errors.New("artifact target must be closed JSON of at most 2048 bytes"), cli.ExitValidation)
	}
	if err := validateArtifactManifestJSON(raw); err != nil {
		return nil, cli.WithExitCode(fmt.Errorf("artifact target: %w", err), cli.ExitValidation)
	}
	// The server's own closed-object parser: unknown fields, a page-less
	// comment or a non-positive base are refused here, before any network.
	intent, err := artifacts.ProjectIntentFromMetadata(map[string]any{artifacts.ProjectIntentMetadataKey: rawJSON(raw)})
	if err != nil || intent == nil {
		return nil, cli.WithExitCode(errors.New("artifact target must be {submission_id, page_id, comment_id, base_version, kind?} or {submission_id} for a new artifact"), cli.ExitValidation)
	}
	return intent, nil
}

// rawJSON marshals as the exact bytes it holds.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) { return r, nil }

// runTargetMetadata reads --document-target / --artifact-target into the
// chat-frame metadata the send carries. At most one target per message.
func runTargetMetadata(cmd *cobra.Command, existingChat string, direct bool) (map[string]any, error) {
	documentFile, _ := cmd.Flags().GetString("document-target")
	artifactFile, _ := cmd.Flags().GetString("artifact-target")
	if documentFile == "" && artifactFile == "" {
		return nil, nil
	}
	if documentFile != "" && artifactFile != "" {
		return nil, cli.WithExitCode(errors.New("--document-target and --artifact-target are mutually exclusive"), cli.ExitValidation)
	}
	if documentFile != "" {
		if existingChat == "" || !direct {
			return nil, fmt.Errorf("--document-target requires one existing --chat and a direct non-interactive run")
		}
		target, err := readRunDocumentTarget(documentFile)
		if err != nil {
			return nil, err
		}
		return map[string]any{"document_iteration_intent": target}, nil
	}
	if existingChat == "" || !direct {
		return nil, cli.WithExitCode(errors.New("--artifact-target requires the artifact's existing source --chat and a direct non-interactive run"), cli.ExitValidation)
	}
	target, err := readRunArtifactTarget(artifactFile)
	if err != nil {
		return nil, err
	}
	return map[string]any{artifacts.ProjectIntentMetadataKey: target}, nil
}

// agentBusyError classifies the per-agent run-lock bounce: nothing was
// delivered or stored, and the remedy is to retry after the live run ends.
func agentBusyError(notice string) error {
	return cli.WithExitCode(fmt.Errorf("agent busy: %s", notice), cli.ExitBusy)
}
