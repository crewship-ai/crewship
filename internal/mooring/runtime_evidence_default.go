//go:build !linux || !mooring_rust_integration

package mooring

import "os/exec"

func actualRuntimeEvidence(cmd *exec.Cmd) (func(), error) { return runtimeStderrEvidence(cmd) }
