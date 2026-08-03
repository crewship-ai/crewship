package devcontainer

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHoster is a provisioner Docker client that knows its endpoint, as the
// real *client.Client does.
type fakeHoster struct {
	CommitClient
	host string
}

func (f fakeHoster) DaemonHost() string { return f.host }

// recordingDockerCLI writes a stand-in `docker` onto PATH that records the
// daemon-selecting environment it was invoked with, and returns the path of the
// file it records into. This is the only way to observe the value that actually
// reaches the build subprocess — asserting on the builder's own field would
// pass just as happily if Build never passed it on.
func recordingDockerCLI(t *testing.T) (bin string, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "record")
	bin = filepath.Join(dir, "docker")
	script := "#!/bin/sh\n" +
		"{\n" +
		"  echo \"DOCKER_HOST=${DOCKER_HOST-<unset>}\"\n" +
		"  echo \"DOCKER_CONTEXT=${DOCKER_CONTEXT-<unset>}\"\n" +
		"  echo \"DOCKER_BUILDKIT=${DOCKER_BUILDKIT-<unset>}\"\n" +
		"} > " + recordPath + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, recordPath
}

func recorded(t *testing.T, path, key string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fake docker CLI never ran (%v)", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	t.Fatalf("no %s recorded in %q", key, raw)
	return ""
}

func stageEmptyBuildContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBuildTargetsTheProvidersDaemon is #1705: the image must be built on the
// same daemon the provisioner cache-checks and creates against, whatever the
// operator's `docker context` says. When it is not, the create looks for a
// local-only tag on the other daemon, tries to pull it, and reports
// `pull access denied ... may require 'docker login'`.
func TestBuildTargetsTheProvidersDaemon(t *testing.T) {
	bin, record := recordingDockerCLI(t)
	const providerEndpoint = "unix:///Users/x/.orbstack/run/docker.sock"

	// The state `colima start` leaves behind: the CLI's own selection points
	// somewhere else entirely.
	t.Setenv("DOCKER_CONTEXT", "colima")
	t.Setenv("DOCKER_HOST", "unix:///Users/x/.colima/default/docker.sock")

	b := NewDockerBuildKitBuilderFor(slog.New(slog.NewTextHandler(io.Discard, nil)),
		fakeHoster{host: providerEndpoint})
	b.bin = bin

	if err := b.Build(context.Background(), stageEmptyBuildContext(t), "crewship-feat:deadbeef", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got := recorded(t, record, "DOCKER_HOST"); got != providerEndpoint {
		t.Errorf("build ran against DOCKER_HOST=%q, want the provider's %q — a split here builds the image into one daemon and creates the container in another (#1705)",
			got, providerEndpoint)
	}
	// DOCKER_CONTEXT surviving would leave a second, independent endpoint
	// selector in the child's environment.
	if got := recorded(t, record, "DOCKER_CONTEXT"); got != "<unset>" {
		t.Errorf("DOCKER_CONTEXT=%q reached the build; the child must see exactly one endpoint selector", got)
	}
	if got := recorded(t, record, "DOCKER_BUILDKIT"); got != "1" {
		t.Errorf("DOCKER_BUILDKIT=%q, want 1 — cache mounts and the dockerfile frontend depend on it", got)
	}
}

// TestBuildStaysUnpinnedWithoutAProviderEndpoint: a client that cannot report
// its daemon (every fake CommitClient in this package) must leave the operator
// environment exactly as it was, not blank the endpoint out.
func TestBuildStaysUnpinnedWithoutAProviderEndpoint(t *testing.T) {
	bin, record := recordingDockerCLI(t)
	t.Setenv("DOCKER_CONTEXT", "colima")

	b := NewDockerBuildKitBuilderFor(slog.New(slog.NewTextHandler(io.Discard, nil)), struct{ CommitClient }{})
	b.bin = bin

	if err := b.Build(context.Background(), stageEmptyBuildContext(t), "crewship-feat:deadbeef", nil); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := recorded(t, record, "DOCKER_CONTEXT"); got != "colima" {
		t.Errorf("unpinned build changed DOCKER_CONTEXT to %q, want it left at colima", got)
	}
}

// TestProvisionerPinsItsBuilder closes the loop the issue actually reported:
// NewProvisioner built the builder from a logger alone, so the endpoint its own
// Docker client had resolved never reached the build.
func TestProvisionerPinsItsBuilder(t *testing.T) {
	const endpoint = "unix:///Users/x/.rd/docker.sock"
	p := NewProvisioner(fakeHoster{host: endpoint}, nil, nil, nil)
	b, ok := p.builder.(*DockerBuildKitBuilder)
	if !ok {
		t.Fatalf("provisioner builder is %T, want *DockerBuildKitBuilder", p.builder)
	}
	if b.host != endpoint {
		t.Errorf("provisioner's builder is pinned to %q, want its own client's %q", b.host, endpoint)
	}
}
