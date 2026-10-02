//go:build integration

package toolchain

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestQualificationThroughDockerRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	img, err := d.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration test requires local Docker alpine:3: %v", err)
	}
	// Build a synthetic CLI without network or provider credentials.
	source, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: img.ID}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	tag := "crewship-qualification-test:" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = d.ContainerRemove(cleanup, source.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		_, _ = d.ImageRemove(cleanup, tag, client.ImageRemoveOptions{Force: true})
	})
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	content := []byte("#!/bin/sh\necho 'codex-cli 0.152.0'\n")
	if err := tw.WriteHeader(&tar.Header{Name: "codex", Mode: 0755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CopyToContainer(ctx, source.ID, client.CopyToContainerOptions{DestinationPath: "/usr/local/bin", Content: &archive}); err != nil {
		t.Fatal(err)
	}
	committed, err := d.ContainerCommit(ctx, source.ID, client.ContainerCommitOptions{Reference: tag})
	if err != nil {
		t.Fatal(err)
	}
	p, err := dockerprovider.New(ctx, dockerprovider.Config{InstanceID: "qualify-test-" + strings.ToLower(rand.Text())}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	inventory := &devcontainer.ToolchainInventory{ImageID: committed.ID, Tools: []devcontainer.ToolchainTool{{Binary: "codex", Path: "/usr/local/bin/codex", Version: "0.152.0", Status: "observed"}}}
	got := Qualify(ctx, p, inventory, "")
	if got.Status != "passed" {
		t.Fatalf("synthetic CLI qualification: %+v", got)
	}
	inventory.Tools[0].Path = "/bin/echo"
	got = Qualify(ctx, p, inventory, "")
	if got.Status != "failed" || len(got.Tools) != 1 || got.Tools[0].Status != "version_mismatch" {
		t.Fatalf("false qualification: %+v", got)
	}
}
