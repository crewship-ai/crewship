//go:build integration

package devcontainer

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestToolchainInventory_RealImageAndCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	if _, err := docker.ImageInspect(ctx, "alpine:3"); err != nil {
		t.Fatalf("integration test requires Docker with local alpine:3: %v", err)
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:3", Cmd: []string{"sleep", "infinity"}},
		HostConfig: &container.HostConfig{NetworkMode: "none"}, Name: tempContainerName(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tag := "crewship-toolchain-test:" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		_, _ = docker.ImageRemove(cleanup, tag, client.ImageRemoveOptions{Force: true})
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(docker, testLogger())
	setup := "mkdir -p /home/agent /usr/local/bin; printf '#!/bin/sh\\necho 2.1.263 \\(Claude Code\\)\\n' > /usr/local/bin/claude; chmod 0755 /usr/local/bin/claude"
	if out, code, err := installer.execInContainerAsUser(ctx, created.ID, []string{"sh", "-c", setup}, "0:0", nil); err != nil || code != 0 {
		t.Fatalf("setup: %v %d %s", err, code, out)
	}
	if err := writeToolchainInventory(ctx, created.ID, []string{"claude"}, nil, installer.execInContainerAsUser); err != nil {
		t.Fatal(err)
	}
	committed, err := docker.ContainerCommit(ctx, created.ID, client.ContainerCommitOptions{Reference: tag})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProvisioner(docker, installer, nil, testLogger())
	for i := 0; i < 2; i++ {
		inventory := p.inspectToolchain(ctx, tag, []string{"claude"})
		if inventory.ImageID != committed.ID || inventory.Status != "recorded" || len(inventory.Tools) != 1 || inventory.Tools[0].Version != "2.1.263" {
			t.Fatalf("image inventory read %d: %+v", i, inventory)
		}
	}
}
