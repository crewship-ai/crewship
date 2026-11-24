//go:build !linux

package docker

import (
	"context"
	"errors"
	"os"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func verifyPodmanMountedFile(context.Context, *client.Client, container.InspectResponse, map[string]bool, string, os.FileInfo) error {
	return errors.New("local Podman mount inspection requires Linux")
}
