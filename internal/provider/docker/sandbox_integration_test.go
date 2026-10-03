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
	"github.com/moby/moby/client"
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
