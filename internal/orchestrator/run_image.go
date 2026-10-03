package orchestrator

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/dockerutil"
)

// observeRunImage records the source of the exact admitted container. It does
// not read crew configuration, run an in-container command, pull an image or
// resolve a tag. This is best-effort provenance, not an execution gate: an
// unavailable provider leaves explicit unknown evidence without blocking work.
func (o *Orchestrator) observeRunImage(ctx context.Context, containerID string) string {
	if o.container == nil || containerID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, err := o.container.ContainerStatus(ctx, containerID)
	if err != nil || status == nil || status.ID != containerID || !dockerutil.IsLocalImageID(status.ImageID) {
		return ""
	}
	return status.ImageID
}
