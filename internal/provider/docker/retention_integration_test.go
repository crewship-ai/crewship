//go:build integration

package docker

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The candidate's tag moves after enumeration. Retention must remove only
// the inspected old image, never the new artifact now carrying that tag.
func TestCacheEvictionRealDockerRetag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	base, err := d.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration requires Docker with local alpine:3: %v", err)
	}
	source, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: base.ID}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.ToLower(rand.Text())
	tag := "crewship-cache:retention-" + token
	var ids []string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = d.ContainerRemove(cleanup, source.ID, client.ContainerRemoveOptions{Force: true})
		_, _ = d.ImageRemove(cleanup, tag, client.ImageRemoveOptions{Force: true})
		for _, id := range ids {
			_, _ = d.ImageRemove(cleanup, id, client.ImageRemoveOptions{Force: true})
		}
	})
	first, err := d.ContainerCommit(ctx, source.ID, client.ContainerCommitOptions{Reference: tag, Changes: []string{"LABEL crewship.retention-fixture=" + token + "-first"}})
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, first.ID)
	p := &Provider{client: d}
	images, err := p.ListCacheImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, img := range images {
		if img.ID == first.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("candidate not enumerated")
	}
	// Commit a distinct successor directly to the moving alias.
	second, err := d.ContainerCommit(ctx, source.ID, client.ContainerCommitOptions{Reference: tag, Changes: []string{"LABEL crewship.retention-fixture=" + token + "-second"}})
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, second.ID)
	if first.ID == second.ID {
		t.Fatal("fixture did not produce distinct artifacts")
	}
	if err := p.RemoveImage(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	successor, err := d.ImageInspect(ctx, tag)
	if err != nil || successor.ID != second.ID {
		t.Fatalf("successor lost: %s %v", successor.ID, err)
	}
	if _, err := d.ImageInspect(ctx, first.ID); err == nil {
		t.Fatal("old candidate was not removed")
	}
}
