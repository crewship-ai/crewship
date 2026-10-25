//go:build !linux || !mooring_rust_integration

package mooring

import "os/exec"

func actualRuntimeEvidence(_ *exec.Cmd) (func(), error) { return func() {}, nil }
