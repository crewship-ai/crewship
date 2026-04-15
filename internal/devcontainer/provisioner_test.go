package devcontainer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// mockCommitClient implements CommitClient for testing.
type mockCommitClient struct {
	// Existing images (by RepoTag).
	existingImages []string
	// Optional RepoDigests per ref, keyed by the inspected reference.
	inspectDigests map[string][]string

	// Recorded calls.
	createdContainers []mockCreateCall
	startedIDs        []string
	stoppedIDs        []string
	removedIDs        []string
	committedIDs      []string
	commitRefs        []string

	// Configurable errors.
	createErr error
	startErr  error
	commitErr error
	listErr   error

	// Call counters.
	imageListCalls int
}

type mockCreateCall struct {
	config  *container.Config
	hostCfg *container.HostConfig
	name    string
}

func (m *mockCommitClient) ContainerCreate(_ context.Context, config *container.Config, hostConfig *container.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	m.createdContainers = append(m.createdContainers, mockCreateCall{
		config:  config,
		hostCfg: hostConfig,
		name:    name,
	})
	if m.createErr != nil {
		return container.CreateResponse{}, m.createErr
	}
	return container.CreateResponse{ID: "temp-container-id"}, nil
}

func (m *mockCommitClient) ContainerStart(_ context.Context, containerID string, _ container.StartOptions) error {
	m.startedIDs = append(m.startedIDs, containerID)
	return m.startErr
}

func (m *mockCommitClient) ContainerStop(_ context.Context, containerID string, _ container.StopOptions) error {
	m.stoppedIDs = append(m.stoppedIDs, containerID)
	return nil
}

func (m *mockCommitClient) ContainerRemove(_ context.Context, containerID string, _ container.RemoveOptions) error {
	m.removedIDs = append(m.removedIDs, containerID)
	return nil
}

func (m *mockCommitClient) ContainerCommit(_ context.Context, containerID string, options container.CommitOptions) (container.CommitResponse, error) {
	m.committedIDs = append(m.committedIDs, containerID)
	m.commitRefs = append(m.commitRefs, options.Reference)
	if m.commitErr != nil {
		return container.CommitResponse{}, m.commitErr
	}
	return container.CommitResponse{ID: "sha256:committed"}, nil
}

func (m *mockCommitClient) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	m.imageListCalls++
	if m.listErr != nil {
		return nil, m.listErr
	}
	var summaries []image.Summary
	for _, tag := range m.existingImages {
		summaries = append(summaries, image.Summary{
			RepoTags: []string{tag},
		})
	}
	return summaries, nil
}

func (m *mockCommitClient) ImagePull(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockCommitClient) ImageInspect(_ context.Context, ref string, _ ...dockerclient.ImageInspectOption) (image.InspectResponse, error) {
	for _, tag := range m.existingImages {
		if tag == ref {
			return image.InspectResponse{RepoDigests: m.inspectDigests[ref]}, nil
		}
	}
	return image.InspectResponse{}, errors.New("no such image")
}

func TestIsCached_Hit(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04"}
	hash := configHash("ubuntu:22.04", cfg, "")
	tag := cacheImageTag(hash)

	mock := &mockCommitClient{existingImages: []string{tag}}
	p := NewProvisioner(mock, nil, nil, testLogger())

	cached, err := p.IsCached(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if !cached {
		t.Error("IsCached() = false, want true")
	}
}

func TestIsCached_Miss(t *testing.T) {
	mock := &mockCommitClient{existingImages: []string{"crewship-cache:other123"}}
	p := NewProvisioner(mock, nil, nil, testLogger())

	cached, err := p.IsCached(context.Background(), "deadbeef1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		t.Error("IsCached() = true, want false")
	}
}

func TestConfigHash_Deterministic(t *testing.T) {
	cfg := &Config{
		Image: "ubuntu:22.04",
		Features: map[string]map[string]any{
			"ghcr.io/devcontainers/features/python:1": {"version": "3.11"},
		},
	}

	h1 := configHash("ubuntu:22.04", cfg, "node 20")
	h2 := configHash("ubuntu:22.04", cfg, "node 20")

	if h1 != h2 {
		t.Errorf("config hash not deterministic: %s != %s", h1, h2)
	}
}

func TestConfigHash_DifferentInputs(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04"}

	h1 := configHash("ubuntu:22.04", cfg, "")
	h2 := configHash("ubuntu:24.04", cfg, "")
	h3 := configHash("ubuntu:22.04", cfg, "node 20")

	if h1 == h2 {
		t.Error("different base images should produce different hashes")
	}
	if h1 == h3 {
		t.Error("different mise configs should produce different hashes")
	}
}

func TestProvision_CacheHit(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04"}
	hash := configHash("ubuntu:22.04", cfg, "")
	tag := cacheImageTag(hash)

	mock := &mockCommitClient{existingImages: []string{tag}}
	p := NewProvisioner(mock, nil, nil, testLogger())

	result, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	if result.CachedImage != tag {
		t.Errorf("CachedImage = %q, want %q", result.CachedImage, tag)
	}
	if result.ConfigHash != hash {
		t.Errorf("ConfigHash = %q, want %q", result.ConfigHash, hash)
	}

	// Should not create any containers.
	if len(mock.createdContainers) != 0 {
		t.Errorf("expected no container creation on cache hit, got %d", len(mock.createdContainers))
	}
}

func TestProvision_EmptyConfig(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04"}

	commitMock := &mockCommitClient{}
	p := NewProvisioner(commitMock, nil, nil, testLogger())

	result, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	if result.CachedImage != "" {
		t.Errorf("expected empty CachedImage for no-op config, got %q", result.CachedImage)
	}
	if result.ConfigHash == "" {
		t.Error("expected non-empty ConfigHash")
	}

	// Should NOT create any containers when config has no customizations.
	if len(commitMock.createdContainers) != 0 {
		t.Errorf("expected no container creation for empty config, got %d", len(commitMock.createdContainers))
	}
}

func TestProvision_NoFeatures(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04", ContainerEnv: map[string]string{"FOO": "bar"}}

	// Mock docker client that also satisfies DockerClient for the installer.
	dockerMock := &mockDockerClient{exitCode: 0}
	commitMock := &mockCommitClient{}
	inst := NewInstaller(dockerMock, testLogger())

	p := NewProvisioner(commitMock, inst, nil, testLogger())

	result, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	if result.CachedImage == "" {
		t.Error("CachedImage is empty")
	}

	// Should create, start, commit, then cleanup.
	if len(commitMock.createdContainers) != 1 {
		t.Fatalf("expected 1 container creation, got %d", len(commitMock.createdContainers))
	}
	if len(commitMock.startedIDs) != 1 {
		t.Errorf("expected 1 container start, got %d", len(commitMock.startedIDs))
	}
	if len(commitMock.committedIDs) != 1 {
		t.Errorf("expected 1 container commit, got %d", len(commitMock.committedIDs))
	}

	// Verify container config.
	createCall := commitMock.createdContainers[0]
	if createCall.config.Image != "ubuntu:22.04" {
		t.Errorf("container image = %q, want ubuntu:22.04", createCall.config.Image)
	}
	if createCall.config.User != "0:0" {
		t.Errorf("container user = %q, want 0:0", createCall.config.User)
	}

	// Cleanup should have been called.
	if len(commitMock.stoppedIDs) == 0 {
		t.Error("expected container stop on cleanup")
	}
	if len(commitMock.removedIDs) == 0 {
		t.Error("expected container remove on cleanup")
	}
}

func TestProvision_CreateContainerError(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04", ContainerEnv: map[string]string{"FOO": "bar"}}

	commitMock := &mockCommitClient{
		createErr: fmt.Errorf("no such image"),
	}
	p := NewProvisioner(commitMock, nil, nil, testLogger())

	_, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "creating temp container") {
		t.Errorf("error = %q, want it to contain 'creating temp container'", err)
	}
}

func TestProvision_StartContainerError(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04", ContainerEnv: map[string]string{"FOO": "bar"}}

	commitMock := &mockCommitClient{
		startErr: fmt.Errorf("OCI runtime error"),
	}
	p := NewProvisioner(commitMock, nil, nil, testLogger())

	_, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "starting temp container") {
		t.Errorf("error = %q, want it to contain 'starting temp container'", err)
	}

	// Should still cleanup.
	if len(commitMock.removedIDs) == 0 {
		t.Error("expected container cleanup after start failure")
	}
}

func TestProvision_CommitError(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04", ContainerEnv: map[string]string{"FOO": "bar"}}

	dockerMock := &mockDockerClient{exitCode: 0}
	commitMock := &mockCommitClient{
		commitErr: fmt.Errorf("no space left on device"),
	}
	inst := NewInstaller(dockerMock, testLogger())

	p := NewProvisioner(commitMock, inst, nil, testLogger())

	_, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "committing container") {
		t.Errorf("error = %q, want it to contain 'committing container'", err)
	}

	// Should still cleanup.
	if len(commitMock.removedIDs) == 0 {
		t.Error("expected container cleanup after commit failure")
	}
}

func TestProvision_ImageListError(t *testing.T) {
	cfg := &Config{Image: "ubuntu:22.04"}

	commitMock := &mockCommitClient{
		listErr: fmt.Errorf("daemon not responding"),
	}
	p := NewProvisioner(commitMock, nil, nil, testLogger())

	_, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "listing images") {
		t.Errorf("error = %q, want it to contain 'listing images'", err)
	}
}

func TestCacheImageTag(t *testing.T) {
	tag := cacheImageTag("a1b2c3d4e5f6g7h8i9j0")
	if tag != "crewship-cache:a1b2c3d4e5f6" {
		t.Errorf("cacheImageTag() = %q, want crewship-cache:a1b2c3d4e5f6", tag)
	}
}

func TestCacheImageTag_ShortHash(t *testing.T) {
	tag := cacheImageTag("abc")
	if tag != "crewship-cache:abc" {
		t.Errorf("cacheImageTag() = %q, want crewship-cache:abc", tag)
	}
}

func TestProvision_PostCreateCommand(t *testing.T) {
	cfg := &Config{
		Image:             "ubuntu:22.04",
		PostCreateCommand: "echo setup complete",
	}

	dockerMock := &mockDockerClient{exitCode: 0}
	commitMock := &mockCommitClient{}
	inst := NewInstaller(dockerMock, testLogger())
	p := NewProvisioner(commitMock, inst, nil, testLogger())

	result, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.CachedImage == "" {
		t.Error("CachedImage is empty")
	}

	// The installer's execInContainer should have been called for
	// postCreateCommand and cleanupCaches.
	if len(dockerMock.execCreated) < 1 {
		t.Errorf("expected at least 1 exec call for postCreateCommand, got %d", len(dockerMock.execCreated))
	}
}

func TestProvision_ContainerEnv(t *testing.T) {
	cfg := &Config{
		Image: "ubuntu:22.04",
		ContainerEnv: map[string]string{
			"GO_ENV": "production",
			"PORT":   "8080",
		},
	}

	dockerMock := &mockDockerClient{exitCode: 0}
	commitMock := &mockCommitClient{}
	inst := NewInstaller(dockerMock, testLogger())
	p := NewProvisioner(commitMock, inst, nil, testLogger())

	_, err := p.Provision(context.Background(), "ubuntu:22.04", cfg, "")
	if err != nil {
		t.Fatal(err)
	}

	// Should have exec calls for containerEnv write and cleanup.
	if len(dockerMock.execCreated) < 2 {
		t.Errorf("expected at least 2 exec calls (containerEnv + cleanup), got %d", len(dockerMock.execCreated))
	}
}

// TestImageListCache_Memoizes verifies that two consecutive imageExists calls
// on the same Provisioner hit Docker only once (cache is shared + fresh).
func TestImageListCache_Memoizes(t *testing.T) {
	mock := &mockCommitClient{existingImages: []string{"crewship-cache:abc"}}
	p := NewProvisioner(mock, nil, nil, testLogger())

	if _, err := p.IsCached(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.IsCached(context.Background(), "def"); err != nil {
		t.Fatal(err)
	}
	if mock.imageListCalls != 1 {
		t.Errorf("expected 1 ImageList call (cache hit), got %d", mock.imageListCalls)
	}
}

// TestImageListCache_InvalidatedBySweeper verifies that invalidateImageListCache
// forces the next listImages call to hit Docker.
func TestImageListCache_InvalidatedBySweeper(t *testing.T) {
	mock := &mockCommitClient{existingImages: []string{"crewship-cache:abc"}}
	p := NewProvisioner(mock, nil, nil, testLogger())

	if _, err := p.IsCached(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	p.invalidateImageListCache()
	if _, err := p.IsCached(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
	if mock.imageListCalls != 2 {
		t.Errorf("expected 2 ImageList calls (post-invalidate), got %d", mock.imageListCalls)
	}
}

// TestCreateTempContainer_HasLabel verifies the temp container carries the
// label the orphan-temp GC sweeper filters on.
func TestCreateTempContainer_HasLabel(t *testing.T) {
	mock := &mockCommitClient{
		existingImages: []string{"ubuntu:22.04"},
		inspectDigests: map[string][]string{"ubuntu:22.04": {"ubuntu@sha256:deadbeef"}},
	}
	p := NewProvisioner(mock, nil, nil, testLogger())

	if _, err := p.createTempContainer(context.Background(), "ubuntu:22.04"); err != nil {
		t.Fatalf("createTempContainer: %v", err)
	}
	if len(mock.createdContainers) != 1 {
		t.Fatalf("expected 1 ContainerCreate call, got %d", len(mock.createdContainers))
	}
	labels := mock.createdContainers[0].config.Labels
	if got := labels[TempContainerLabelKey]; got != TempContainerLabelValue {
		t.Errorf("temp container missing GC label: got %q, want %q", got, TempContainerLabelValue)
	}
}
