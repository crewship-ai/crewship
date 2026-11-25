//go:build !linux

package backup

import (
	"context"

	"github.com/crewship-ai/crewship/internal/writerlease"
)

type forceStage struct {
	dir, previous string
	skipped       []string
}

func beginForceStage(context.Context, string) (*forceStage, error) {
	return nil, writerlease.ErrUnsupportedStorage
}
func (*forceStage) commit(context.Context) (bool, error) {
	return false, writerlease.ErrUnsupportedStorage
}
func (*forceStage) close() {}
