//go:build !linux

package docker

import "errors"

func hostProcessIdentity(int, int) (int, uint64, error) {
	return 0, 0, errors.New("host process stop is only qualified on Linux")
}
