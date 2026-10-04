//go:build !unix

package runoutput

import (
	"errors"
	"os"
	"os/exec"
)

func configureProcess(*exec.Cmd) error {
	return errors.New("run capture requires a Unix container runtime")
}

func ExecutionSignals() []os.Signal { return []os.Signal{os.Interrupt} }
