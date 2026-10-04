package orchestrator

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/provider"
)

func (o *Orchestrator) retainRuntimeUse(ctx context.Context, req AgentRunRequest) (*provider.RuntimeUse, error) {
	if req.RuntimeUse != nil {
		if !req.RuntimeUse.Matches(req.CrewID, req.ContainerID) {
			return nil, fmt.Errorf("runtime reservation does not match this run")
		}
		return req.RuntimeUse.Retain()
	}
	if req.ContainerID == "" {
		return nil, nil
	}
	if reserving, ok := o.container.(provider.CrewRuntimeUseProvider); ok {
		return reserving.RetainCrewRuntimeUse(ctx, req.CrewID, req.ContainerID)
	}
	return nil, nil
}
