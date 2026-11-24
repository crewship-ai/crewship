//go:build !linux

package dockerutil

import "context"

func rootlessPodmanOOMScoreAdj(context.Context, string) (int, error) { return 0, nil }
