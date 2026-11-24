//go:build !linux

package dockerutil

import "context"

func rootlessPodmanOOMScoreAdj(context.Context, any, string) (int, error) { return 0, nil }
