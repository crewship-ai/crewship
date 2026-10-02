//go:build !linux

package egressfence

// Apply is unavailable off linux; the crew container's namespace is always a
// linux one, and the sidecar binary that runs this is always a linux build.
func Apply(Spec) error { return ErrNotSupported }

// Check is unavailable off linux.
func Check() (State, error) { return State{}, ErrNotSupported }
