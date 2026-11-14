package devcontainer

import "context"

// BuildPreflight decides whether a build may start writing image data. It is
// consulted once per provision, after the cache lookup and before the base
// image is pulled or any layer is written, so a cache hit is never refused.
// A non-nil error fails the provision with that error; nothing is cleaned up
// or deleted on its behalf.
type BuildPreflight func(ctx context.Context) error

// ProvStepStoragePreflight is the Detail of the provision.failed event when a
// build is refused before it starts (see BuildPreflight).
const ProvStepStoragePreflight = "storage_preflight"

// SetBuildPreflight installs the check run before every build. nil removes
// it. Call before the first Provision; it is not synchronised against one.
func (p *Provisioner) SetBuildPreflight(check BuildPreflight) {
	p.buildPreflight = check
}

// HasBuildPreflight reports whether a check is installed.
func (p *Provisioner) HasBuildPreflight() bool { return p.buildPreflight != nil }

func (p *Provisioner) checkBuildPreflight(ctx context.Context) error {
	if p.buildPreflight == nil {
		return nil
	}
	return p.buildPreflight(ctx)
}
