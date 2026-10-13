//go:build !unix

package runoutput

import (
	"context"
	"errors"
	"os/exec"
)

func configureHostHelper(*exec.Cmd) error { return errors.New("protected host capture requires Unix") }
func validateHostDirectory(string) error  { return errors.New("protected host capture requires Unix") }

func lockHostReplay(context.Context, string) (func(), error) {
	return nil, errors.New("protected host replay requires Unix")
}
