package orchestrator

import (
	"context"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/provider"
)

const managedGeminiSettingsFile = ".gemini/crewship-managed-settings.json"

// System settings take precedence over user/workspace settings, including an
// untrusted workspace. Keep old inverted names for older provisioned Gemini
// versions; current releases migrate them and prefer the explicit new names.
// This config controls cooperative updater behavior, not hostile shell code.
const managedGeminiSettings = `{"general":{"enableAutoUpdate":false,"enableAutoUpdateNotification":false,"disableAutoUpdate":true,"disableUpdateNag":true}}`

func setupManagedToolchainSettings(ctx context.Context, container provider.ContainerProvider, req AgentRunRequest, logger *slog.Logger) error {
	if req.CLIAdapter != "GEMINI_CLI" {
		return nil
	}
	return writeFileViaContainer(ctx, container, req.ContainerID, agentHomeDir(req.AgentSlug, req.RunID), managedGeminiSettingsFile, managedGeminiSettings, containerFileSecret, logger)
}
