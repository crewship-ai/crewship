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
// eviction, a manual `docker rmi`), and returns that image: the tag it
// verified, which a rebuild may have changed. It returns "" when the crew has
// no provisioned image (no build needed, or a crew it does not know).
type ImageGate func(ctx context.Context, crewID string) (string, error)

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

// waitForImage reports whether a gate ran, the image it verified, and its
// failure.
func waitForImage(ctx context.Context, crewID string) (bool, string, error) {
	gate := imageGate.Load()
	if gate == nil || crewID == "" {
		return false, "", nil
	}
	image, err := (*gate)(ctx, crewID)
	if err != nil {
		return true, "", fmt.Errorf("%w: %w", ErrImageNotReady, err)
	}
	return true, image, nil
}

// ErrPartialConfig matches a completion that is usable although part of it
// failed (PartialConfigError): the crew's image, mounts and limits resolved,
// only something optional like its sidecar services did not.
var ErrPartialConfig = errors.New("crew runtime config partially resolved")

// PartialConfigError is a completer error that leaves the returned config
// usable. Once the image gate has run, every OTHER completer error refuses the
// start: the config the gate verified could not be read, and neither the
// caller's tag nor the default image is a substitute for it.
type PartialConfigError string

func (e PartialConfigError) Error() string { return string(e) }

// Is makes errors.Is(err, ErrPartialConfig) true for every PartialConfigError.
func (e PartialConfigError) Is(target error) bool { return target == ErrPartialConfig }
