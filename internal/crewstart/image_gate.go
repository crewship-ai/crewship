package crewstart

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// ErrImageNotReady wraps a failure of the image gate: the crew's provisioned
// image is missing and could not be (re)built, so its runtime was not started.
var ErrImageNotReady = errors.New("crew image not ready")

// ImageGate blocks until the crew's provisioned image exists locally,
// building it when it was never built or has since been removed (idle cache
// eviction, a manual `docker rmi`). It returns nil when no build is needed.
type ImageGate func(ctx context.Context, crewID string) error

// imageGate is process-wide, like the contract it guards: the Starters are
// built at eight sites and three of them (pipeline, orchestrator, terminal)
// sit in packages that cannot import internal/api, where provisioning lives.
// A per-Starter field would have to be threaded through all eight and would
// be forgotten at the ninth — the defect this package exists to prevent.
var imageGate atomic.Pointer[ImageGate]

// SetImageGate installs the gate every Start consults before it starts a
// crew's runtime. Called once at server bootstrap; nil removes it (tests,
// headless harnesses), which starts whatever image the config names.
func SetImageGate(gate ImageGate) {
	if gate == nil {
		imageGate.Store(nil)
		return
	}
	imageGate.Store(&gate)
}

// waitForImage reports whether a gate ran, and its failure.
func waitForImage(ctx context.Context, crewID string) (bool, error) {
	gate := imageGate.Load()
	if gate == nil || crewID == "" {
		return false, nil
	}
	if err := (*gate)(ctx, crewID); err != nil {
		return true, fmt.Errorf("%w: %w", ErrImageNotReady, err)
	}
	return true, nil
}
