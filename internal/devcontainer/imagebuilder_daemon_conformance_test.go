//go:build conformance

// Which daemon does the image actually land on? (#1705)
//
// The provisioner builds with the `docker` CLI (endpoint chosen by
// `docker context`) and cache-checks + creates through the provider's SDK
// client (endpoint chosen by Detect). Nothing reconciled them, and on a machine
// with more than one runtime the default state after `colima start` or a
// Rancher Desktop launch is that they disagree — the image builds into one
// daemon and the create looks for it in another, then tries to pull the
// local-only tag from a registry and reports `pull access denied ... may
// require 'docker login'`.
//
// A unit test can prove the environment handed to the subprocess. Only this can
// prove where the layers ended up.
//
// Run (needs two reachable daemons):
//
//	CREWSHIP_CONFORMANCE_DAEMON_A=unix://$HOME/.orbstack/run/docker.sock \
//	CREWSHIP_CONFORMANCE_DAEMON_B=unix://$HOME/.colima/default/docker.sock \
//	  go test -tags conformance -run TestBuildLandsOnTheProvidersDaemon -v ./internal/devcontainer/

package devcontainer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

func TestBuildLandsOnTheProvidersDaemon(t *testing.T) {
	providerEndpoint := os.Getenv("CREWSHIP_CONFORMANCE_DAEMON_A")
	otherEndpoint := os.Getenv("CREWSHIP_CONFORMANCE_DAEMON_B")
	if providerEndpoint == "" || otherEndpoint == "" {
		t.Fatal("set CREWSHIP_CONFORMANCE_DAEMON_A (the provider's daemon) and CREWSHIP_CONFORMANCE_DAEMON_B (a different one) — this test is about two daemons disagreeing and cannot be run against one")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	providerCli, err := client.New(client.WithHost(providerEndpoint))
	if err != nil {
		t.Fatalf("client for %s: %v", providerEndpoint, err)
	}
	defer providerCli.Close()
	otherCli, err := client.New(client.WithHost(otherEndpoint))
	if err != nil {
		t.Fatalf("client for %s: %v", otherEndpoint, err)
	}
	defer otherCli.Close()
	for name, cli := range map[string]*client.Client{"A": providerCli, "B": otherCli} {
		if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
			t.Fatalf("daemon %s (%s) unreachable: %v", name, cli.DaemonHost(), err)
		}
	}

	// The split, reproduced: the CLI's own selection points at daemon B while
	// the provider's client is on daemon A. DOCKER_CONTEXT is the mechanism
	// `colima start` uses; DOCKER_HOST is set to B as well so nothing but the
	// builder's own pinning can save this.
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_HOST", otherEndpoint)

	tag := fmt.Sprintf("crewship-feat:conf1705-%d", time.Now().UnixNano())
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"),
		[]byte("FROM scratch\nCOPY Dockerfile /Dockerfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := NewDockerBuildKitBuilderFor(slog.New(slog.NewTextHandler(io.Discard, nil)), providerCli)
	if !b.Available() {
		t.Fatal("no docker CLI on PATH — this test is about which daemon that CLI talks to")
	}
	// BuildKit needs the buildx plugin; without it `docker build` exits 1 with
	// a message about buildx that has nothing to do with what this test is
	// measuring. Say which it is before spending a build on it.
	probe := exec.CommandContext(ctx, b.bin, "buildx", "version")
	probe.Env = buildEnv(os.Environ(), b.host)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("`docker buildx version` fails in this environment, so no BuildKit build can run here — that is a host setup problem, not a #1705 result: %v\n%s", err, out)
	} else {
		t.Logf("buildx: %s", out)
	}
	if err := b.Build(ctx, contextDir, tag, func(line string) { t.Log(line) }); err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), time.Minute)
		defer rmCancel()
		_, _ = providerCli.ImageRemove(rmCtx, tag, client.ImageRemoveOptions{Force: true, PruneChildren: true})
		_, _ = otherCli.ImageRemove(rmCtx, tag, client.ImageRemoveOptions{Force: true, PruneChildren: true})
	}()

	onProvider := hasImage(ctx, t, providerCli, tag)
	onOther := hasImage(ctx, t, otherCli, tag)
	t.Logf("tag %s — provider daemon (%s): %t | other daemon (%s): %t", tag, providerEndpoint, onProvider, otherEndpoint, onOther)

	if !onProvider {
		t.Errorf("the image was NOT built on the provider's daemon %s — the container create would go looking for a local-only tag there, fail, and report it as `pull access denied ... may require 'docker login'` (#1705)", providerEndpoint)
	}
	if onOther {
		t.Errorf("the image landed on %s, the daemon the CLI context pointed at rather than the one the provider dialled (#1705)", otherEndpoint)
	}
}

func hasImage(ctx context.Context, t *testing.T, cli *client.Client, tag string) bool {
	t.Helper()
	res, err := cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		t.Fatalf("image list on %s: %v", cli.DaemonHost(), err)
	}
	for _, img := range res.Items {
		for _, rt := range img.RepoTags {
			if rt == tag {
				return true
			}
		}
	}
	return false
}
