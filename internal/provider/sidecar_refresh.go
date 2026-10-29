package provider

import "context"

// SidecarRecreatedEvent reports that a crew runtime was replaced because it
// was created with a different crewship-sidecar binary than the one this
// server stages now (#3964). The sidecar is bind-mounted, and a mount pins the
// inode it was created with, so after an upgrade only a new container picks up
// the new sidecar.
type SidecarRecreatedEvent struct {
	CrewID   string
	CrewSlug string
	// PreviousContainerID is the runtime that was removed.
	PreviousContainerID string
	// ContainerID is the runtime created in its place.
	ContainerID string
	// PreviousSidecarHash is the hash the removed runtime was stamped with,
	// or "" when it predates the stamp.
	PreviousSidecarHash string
	// SidecarHash is the hash of the sidecar binary the new runtime mounts.
	SidecarHash string
}

// SidecarRecreatedReporter is implemented by providers that replace crew
// runtimes serving an outdated sidecar on their own. The hook runs
// synchronously after the replacement started; it must not block for long.
type SidecarRecreatedReporter interface {
	SetSidecarRecreatedHook(func(context.Context, SidecarRecreatedEvent))
}
