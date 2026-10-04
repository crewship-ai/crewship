//go:build !linux

package stagedstart

import "errors"

// Initialize requires a separately qualified Linux ownership helper.
func Initialize([]string) error { return errors.New("staged init: unsupported host") }
