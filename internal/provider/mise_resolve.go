package provider

import (
	"context"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// CrewMiseLockResolver uses a server-selected immutable, installation-owned
// crew image for a bounded proposal. It must never change a crew or run its
// image entrypoint, forward credentials/config env, or mount host files.
type CrewMiseLockResolver interface {
	ResolveCrewMiseLock(context.Context, string, *devcontainer.MiseConfig, bool) (*devcontainer.MiseResolution, error)
}
