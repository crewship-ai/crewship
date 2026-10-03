//go:build integration

package docker

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"slices"
)

func TestSandboxRuntimeRealDocker(t *testing.T) {
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
	p := &Provider{client: d, cfg: Config{InstanceID: "sandbox-test-" + strings.ToLower(rand.Text())}}
	spec := sandboxTestSpec()
	spec.ID = "instance-" + strings.ToLower(rand.Text())
	spec.ImageID = img.ID
	ref, err := p.CreateSandbox(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := p.RemoveSandbox(cleanup, ref); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	out, err := p.ExecSandbox(ctx, ref, provider.SandboxExec{Command: []string{"/bin/sh", "-c", "id -u; test ! -e /var/run/docker.sock; test ! -w /etc; echo workspace > /home/agent/check; cat /home/agent/check; ip route; pid=$(pidof sleep); test -n \"$pid\"; ! kill -STOP \"$pid\" 2>/dev/null"}, Timeout: 5 * time.Second, OutputLimit: 1024})
	if err != nil || out.ExitCode != 0 || strings.TrimSpace(out.Output) != "1001\nworkspace" {
		t.Fatalf("offline probe: %+v %v", out, err)
	}
	if _, err := p.CreateSandbox(ctx, spec); err == nil {
		t.Fatal("duplicate instance identity created")
	}
	foreign := ref
	foreign.ID = "another-instance"
	if err := p.RemoveSandbox(ctx, foreign); !errors.Is(err, provider.ErrSandboxDenied) {
		t.Fatalf("foreign remove: %v", err)
	}
	_, err = p.ExecSandbox(ctx, ref, provider.SandboxExec{Command: []string{"/bin/sh", "-c", "yes excessive"}, Timeout: 5 * time.Second, OutputLimit: 16})
	if !errors.Is(err, provider.ErrSandboxOutputLimit) {
		t.Fatalf("output limit: %v", err)
	}
}

// Image HEALTHCHECK is a separate execution path from its entrypoint. It must
// not leak into version-only qualification or run as the lifetime keeper UID.
func TestSandboxDisablesImageHealthcheckRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	img, err := d.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration requires local Docker alpine:3: %v", err)
	}
	source, err := d.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: img.ID}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	tag := "crewship-healthcheck-test:" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = d.ContainerRemove(cleanup, source.ID, client.ContainerRemoveOptions{Force: true})
		_, _ = d.ImageRemove(cleanup, tag, client.ImageRemoveOptions{Force: true})
	})
	built, err := d.ContainerCommit(ctx, source.ID, client.ContainerCommitOptions{Reference: tag, Config: &container.Config{Healthcheck: &container.HealthConfig{Test: []string{"CMD-SHELL", "touch /tmp/image-healthcheck-ran"}, Interval: time.Second, Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := d.ImageInspect(ctx, built.ID)
	if err != nil || base.Config.Healthcheck == nil || !slices.Equal(base.Config.Healthcheck.Test, []string{"CMD-SHELL", "touch /tmp/image-healthcheck-ran"}) {
		t.Fatalf("image fixture has no healthcheck: %v", err)
	}
	p := &Provider{client: d, cfg: Config{InstanceID: "healthcheck-test-" + strings.ToLower(rand.Text())}}
	spec := sandboxTestSpec()
	spec.ID = "health-" + strings.ToLower(rand.Text())
	spec.ImageID = built.ID
	ref, err := p.CreateSandbox(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := p.RemoveSandbox(cleanup, ref); err != nil {
			t.Error(err)
		}
	})
	actual, err := d.ContainerInspect(ctx, ref.RuntimeID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if actual.Container.Config.Healthcheck == nil || !slices.Equal(actual.Container.Config.Healthcheck.Test, []string{"NONE"}) {
		t.Fatal("sandbox inherited image healthcheck")
	}
	result, err := p.ExecSandbox(ctx, ref, provider.SandboxExec{Command: []string{"/bin/sh", "-c", "sleep 3; test ! -e /tmp/image-healthcheck-ran"}, Timeout: 5 * time.Second, OutputLimit: 1024})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("unexpected image healthcheck executed: %+v %v", result, err)
	}
}
