package docker

import (
	"context"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// ResetOwnedResources removes only exact installation-labelled resources.
// Unlabelled/foreign volumes, images and build cache are never pruned. Docker
// failures stop reset before application DB rows or host data are cleared.
func ResetOwnedResources(ctx context.Context, instance string) error {
	if instance == "" {
		return fmt.Errorf("reset requires an existing installation identity")
	}
	detected, err := Detect(ctx)
	if err != nil {
		return fmt.Errorf("reset requires Docker inventory: %w", err)
	}
	cli, err := client.New(client.FromEnv, client.WithHost(detected.Host))
	if err != nil {
		return err
	}
	defer cli.Close()
	return resetOwnedDocker(ctx, cli, instance)
}

func resetOwnedDocker(ctx context.Context, cli *client.Client, instance string) error {
	containers, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, listed := range containers.Items {
		if listed.Labels[resourcelifecycle.InstanceLabel] != instance {
			continue
		}
		current, err := cli.ContainerInspect(ctx, listed.ID, client.ContainerInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		c := current.Container
		if c.ID != listed.ID || c.Config == nil || c.Config.Labels[resourcelifecycle.InstanceLabel] != instance {
			return fmt.Errorf("container ownership changed during reset")
		}
		timeout := 10
		if _, err := cli.ContainerStop(ctx, c.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("stop owned container %s: %w", c.ID, err)
		}
		if _, err := cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: false, RemoveVolumes: false}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove owned container %s: %w", c.ID, err)
		}
	}
	volumes, err := cli.VolumeList(ctx, volumeListOptions())
	if err != nil {
		return err
	}
	if len(volumes.Warnings) != 0 {
		return fmt.Errorf("incomplete Docker volume inventory; reset aborted")
	}
	for _, listed := range volumes.Items {
		if listed.Labels[resourcelifecycle.InstanceLabel] != instance {
			continue
		}
		current, err := cli.VolumeInspect(ctx, listed.Name, client.VolumeInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if current.Volume.Name != listed.Name || current.Volume.Labels[resourcelifecycle.InstanceLabel] != instance {
			return fmt.Errorf("volume ownership changed during reset")
		}
		if _, err := cli.VolumeRemove(ctx, listed.Name, client.VolumeRemoveOptions{Force: false}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove owned volume %s: %w", listed.Name, err)
		}
	}
	networks, err := cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return err
	}
	for _, listed := range networks.Items {
		if listed.Labels[resourcelifecycle.InstanceLabel] != instance {
			continue
		}
		current, err := cli.NetworkInspect(ctx, listed.ID, client.NetworkInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if current.Network.ID != listed.ID || current.Network.Labels[resourcelifecycle.InstanceLabel] != instance {
			return fmt.Errorf("network ownership changed during reset")
		}
		if _, err := cli.NetworkRemove(ctx, listed.ID, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove owned network %s: %w", listed.ID, err)
		}
	}
	return nil
}
