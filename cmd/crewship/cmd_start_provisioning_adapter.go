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
