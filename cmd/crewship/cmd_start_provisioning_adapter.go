//go:build !clionly

package main

import (
	"context"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chatbridge"
)

// provisioningAdapter bridges *api.ProvisioningHandler.EnqueueForCrew
// to the chatbridge.ProvisioningEnqueuer interface, translating between
// api.EnqueueResult and chatbridge.ProvisioningEnqueueResult. The two
// structs are intentionally not aliased so chatbridge stays free of
// any api-package dependency (the api package already imports chatbridge
// for ChatHandler — flipping that would create a cycle).
type provisioningAdapter struct {
	h *api.ProvisioningHandler
}

func (a provisioningAdapter) EnqueueForCrew(ctx context.Context, crewID, workspaceID string) (chatbridge.ProvisioningEnqueueResult, error) {
	res, err := a.h.EnqueueForCrew(ctx, crewID, workspaceID)
	if err != nil {
		return chatbridge.ProvisioningEnqueueResult{}, err
	}
	return chatbridge.ProvisioningEnqueueResult{
		Started:        res.Started,
		AlreadyRunning: res.AlreadyRunning,
		Status:         res.Status,
	}, nil
}

// AttachPendingMessage passes straight through: unlike EnqueueResult,
// chatbridge.PendingChatMessage is already the exact type
// api.ProvisioningHandler.AttachPendingMessage takes (the api package imports
// chatbridge, not the other way round), so there is no shape to translate.
func (a provisioningAdapter) AttachPendingMessage(crewID string, msg chatbridge.PendingChatMessage) bool {
	return a.h.AttachPendingMessage(crewID, msg)
}

// InvalidateImageCache forwards the bridge's "the daemon says this image is
// gone" signal to the provisioner's memoised image list (#2431). Without it
// the bridge's optional-interface assertion never matches the production
// enqueuer and the invalidation only ever runs in tests.
func (a provisioningAdapter) InvalidateImageCache() {
	a.h.InvalidateImageCache()
}
