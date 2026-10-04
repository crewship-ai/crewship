//go:build integration

package devcontainer

import (
	"context"
	"crypto/rand"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/dockerutil"
)

// This exercises the actual provisioning/commit/cache path. The synthetic
// installer writes a different file on each build; old published IDs must keep
// their old bytes even after the shared cache alias advances.
func TestProvisionImmutableArtifact_RealRebuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	if _, err := docker.ImageInspect(ctx, "alpine:3"); err != nil {
		t.Fatalf("integration requires Docker with local alpine:3: %v", err)
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:3", Cmd: []string{"sleep", "infinity"}},
		HostConfig: &container.HostConfig{NetworkMode: "none"}, Name: tempContainerName(),
	})
	if err != nil {
		t.Fatal(err)
	}
	baseTag := "crewship-artifact-test:" + strings.ToLower(rand.Text())
	var artifacts []string
	var cacheTag string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_, _ = docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
		for _, ref := range append(artifacts, cacheTag, baseTag) {
			if ref != "" {
				_, _ = docker.ImageRemove(cleanup, ref, client.ImageRemoveOptions{Force: true})
			}
		}
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	installer := NewInstaller(docker, testLogger())
	// Provisioning expects bash. The fixture only uses POSIX shell syntax,
	// so a forwarding shim avoids installing packages or contacting a registry.
	setup := "printf '#!/bin/sh\\nexec /bin/sh \"$@\"\\n' > /bin/bash; chmod 0755 /bin/bash"
	if out, code, err := installer.execInContainerAsUser(ctx, created.ID, []string{"sh", "-c", setup}, "0:0", nil); err != nil || code != 0 {
		t.Fatalf("setup: %v %d %s", err, code, out)
	}
	if _, err := docker.ContainerCommit(ctx, created.ID, client.ContainerCommitOptions{Reference: baseTag}); err != nil {
		t.Fatal(err)
	}
	p := NewProvisioner(docker, installer, nil, testLogger())
	p.digestResolver = dockerutil.NewDigestResolver(0, time.Nanosecond)
	cfg := &Config{Image: baseTag, PostCreateCommand: "head -c 32 /dev/urandom | od -An -tx1 > /home/agent/installed-version"}
	readMarker := func(ref string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--user", "1001:1001", "--entrypoint", "cat", ref, "/home/agent/installed-version").CombinedOutput()
		if err != nil {
			t.Fatalf("read marker %s: %v %s", ref, err, out)
		}
		return string(out)
	}
	first, err := p.Provision(ctx, baseTag, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	artifacts = append(artifacts, first.CachedImage)
	cacheTag = cacheImageTag(first.ConfigHash)
	before := readMarker(first.CachedImage)
	second, err := p.Provision(ctx, baseTag, cfg, "", WithForceRebuild(true))
	if err != nil {
		t.Fatal(err)
	}
	artifacts = append(artifacts, second.CachedImage)
	if first.CachedImage == second.CachedImage || !dockerutil.IsLocalImageID(second.CachedImage) {
		t.Fatalf("rebuild did not publish a distinct immutable artifact: %s %s", first.CachedImage, second.CachedImage)
	}
	if readMarker(first.CachedImage) != before || readMarker(second.CachedImage) == before {
		t.Fatal("rebuild changed old artifact or reused old installation")
	}
	cached, err := p.Provision(ctx, baseTag, cfg, "")
	if err != nil || cached.CachedImage != second.CachedImage {
		t.Fatalf("cache did not resolve current immutable artifact: %+v %v", cached, err)
	}
}
