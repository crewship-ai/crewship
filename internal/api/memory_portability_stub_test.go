package api

import (
	"context"
	"io"

	"github.com/crewship-ai/crewship/internal/backup"
)

// stubDockerOps satisfies backup.DockerOps for placer-selection tests,
// which never reach the daemon.
type stubDockerOps struct{}

func (stubDockerOps) Pause(context.Context, string) error   { return nil }
func (stubDockerOps) Unpause(context.Context, string) error { return nil }
func (stubDockerOps) CopyFrom(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (stubDockerOps) CopyTo(context.Context, string, string, io.Reader) error { return nil }
func (stubDockerOps) CopyToPath(context.Context, string, backup.ExtractSpec, io.Reader) error {
	return nil
}
func (stubDockerOps) ContainerExists(context.Context, string) (bool, error) { return true, nil }
func (stubDockerOps) Exec(context.Context, string, []string) (int, []byte, error) {
	return 0, nil, nil
}
func (stubDockerOps) ExecAs(context.Context, string, string, []string) (int, []byte, error) {
	return 0, nil, nil
}
