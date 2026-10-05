//go:build !linux

package managedlaunch

import "errors"

func Launch(string, []string) error { return errors.New("managed launch: Linux launcher required") }
