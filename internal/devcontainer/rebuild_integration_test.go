//go:build integration

package devcontainer

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The synthetic installation writes a fresh random value whenever its RUN
// executes. No provider credentials, package registry, or user container is used.
func TestExplicitRebuild_RealLayerCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "alpine:3").CombinedOutput(); err != nil {
		t.Fatalf("integration test requires local alpine:3 and Docker: %v: %s", err, out)
	}
	dir := t.TempDir()
	tag := "crewship-rebuild-test:" + strings.ToLower(rand.Text())
	content := "FROM alpine:3\nRUN head -c 32 /dev/urandom | od -An -tx1 > /installed-version\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "image", "rm", "--force", tag).Run()
	})
	builder := NewDockerBuildKitBuilder(testLogger())
	var layers []string
	for _, force := range []bool{false, false, true} {
		if err := buildProvisionImage(ctx, builder, dir, tag, func(line string) { t.Log(line) }, force); err != nil {
			t.Fatal(err)
		}
		out, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{json .RootFS.Layers}}", tag).Output()
		if err != nil {
			t.Fatal(err)
		}
		layers = append(layers, strings.TrimSpace(string(out)))
	}
	if layers[0] != layers[1] {
		t.Fatalf("ordinary build did not reuse its layer: %v", layers)
	}
	if layers[1] == layers[2] {
		t.Fatalf("explicit rebuild reused installation layer: %v", layers)
	}
}
