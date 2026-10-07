package provider

import (
	"context"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
)

// ManagedLaunchProvider attests the exact reserved runtime before launch.
// Unsupported providers must refuse an opted-in crew, never use legacy exec.
type ManagedLaunchProvider interface {
	AttestManagedLaunch(context.Context, string, managedlaunch.Descriptor) error
}
