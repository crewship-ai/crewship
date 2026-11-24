//go:build !linux

package dockerutil

import (
	"context"
	"errors"
	"net"
)

func dialPodmanBinds(context.Context, string, []string) (net.Conn, error) {
	return nil, errors.New("local Podman service identity for SELinux relabel requires Linux")
}

func rootlessPodmanOOMScoreAdj(context.Context, any, string) (int, error) { return 0, nil }
