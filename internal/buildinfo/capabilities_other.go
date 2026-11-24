//go:build !linux

package buildinfo

import "errors"

func RequireEmptyProcessCapabilities() error {
	return errors.New("agent capability attestation requires Linux")
}
