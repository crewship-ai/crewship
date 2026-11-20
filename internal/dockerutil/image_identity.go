package dockerutil

import (
	"context"
	"fmt"

	"github.com/moby/moby/client"
)

// VerifyDaemonImageID confirms an engine-returned ID through an exact image
// lookup. The inspected ID must identify the same image configuration.
func VerifyDaemonImageID(ctx context.Context, inspector interface {
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error)
}, returnedID string) (string, error) {
	id := NormalizeDaemonImageID(returnedID)
	if id == "" {
		return "", fmt.Errorf("runtime returned an invalid immutable image ID")
	}
	image, err := inspector.ImageInspect(ctx, id)
	if err != nil {
		return "", fmt.Errorf("inspect immutable image ID: %w", err)
	}
	if NormalizeDaemonImageID(image.ID) != id {
		return "", fmt.Errorf("runtime returned a different image for the immutable ID")
	}
	return id, nil
}
