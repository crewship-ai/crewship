package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	// Recorded calls.
	copiedTars      []copiedTar
	execCreated     []container.ExecOptions
	execAttachCalls int

	// Configurable responses.
	execCreateErr  error
	execAttachErr  error
	execInspectErr error
	copyErr        error
	exitCode       int
	execOutput     string
}

type copiedTar struct {
	containerID string
	dstPath     string
	data        []byte
}

func (m *mockDockerClient) ContainerExecCreate(_ context.Context, _ string, config container.ExecOptions) (container.ExecCreateResponse, error) {
	m.execCreated = append(m.execCreated, config)
	if m.execCreateErr != nil {
		return container.ExecCreateResponse{}, m.execCreateErr
	}
	return container.ExecCreateResponse{ID: "exec-123"}, nil
}

func (m *mockDockerClient) ContainerExecAttach(_ context.Context, _ string, _ container.ExecStartOptions) (types.HijackedResponse, error) {
	m.execAttachCalls++
	if m.execAttachErr != nil {
		return types.HijackedResponse{}, m.execAttachErr
	}
	return types.NewHijackedResponse(newMockConn(m.execOutput), "application/vnd.docker.raw-stream"), nil
}

func (m *mockDockerClient) ContainerExecInspect(_ context.Context, _ string) (container.ExecInspect, error) {
	if m.execInspectErr != nil {
		return container.ExecInspect{}, m.execInspectErr
	}
	return container.ExecInspect{ExitCode: m.exitCode}, nil
}

func (m *mockDockerClient) CopyToContainer(_ context.Context, containerID, dstPath string, content io.Reader, _ container.CopyToContainerOptions) error {
	if m.copyErr != nil {
		return m.copyErr
	}
	data, _ := io.ReadAll(content)
	m.copiedTars = append(m.copiedTars, copiedTar{
		containerID: containerID,
		dstPath:     dstPath,
		data:        data,
	})
	return nil
}

// mockConn implements net.Conn with a buffered reader for testing.
type mockConn struct {
	bytes.Buffer
}

func newMockConn(output string) *mockConn {
	c := &mockConn{}
	c.WriteString(output)
	return c
}

func (c *mockConn) Close() error                       { return nil }
func (c *mockConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *mockConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *mockConn) SetDeadline(_ time.Time) error      { return nil }
func (c *mockConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *mockConn) SetWriteDeadline(_ time.Time) error { return nil }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestInstallFeature_Success(t *testing.T) {
	// Create a temp dir with a fake install.sh.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash\necho installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-feature.json"), []byte(`{"id":"python","version":"1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{exitCode: 0}
	inst := NewInstaller(mock, testLogger())

	feature := &ResolvedFeature{
		Ref: "ghcr.io/devcontainers/features/python:1",
		Dir: dir,
		Metadata: FeatureMetadata{
			ID:      "python",
			Version: "1",
		},
	}

	err := inst.InstallFeature(context.Background(), "container-abc", feature, map[string]any{"version": "3.11"})
	if err != nil {
		t.Fatalf("InstallFeature() error = %v", err)
	}

	// Verify tar was copied.
	if len(mock.copiedTars) != 1 {
		t.Fatalf("expected 1 CopyToContainer call, got %d", len(mock.copiedTars))
	}
	if mock.copiedTars[0].containerID != "container-abc" {
		t.Errorf("CopyToContainer containerID = %q, want %q", mock.copiedTars[0].containerID, "container-abc")
	}
	if mock.copiedTars[0].dstPath != "/tmp/devcontainer-features" {
		t.Errorf("CopyToContainer dstPath = %q, want /tmp/devcontainer-features", mock.copiedTars[0].dstPath)
	}

	// Verify exec calls: install.sh + rm -rf cleanup = 2 exec creates.
	if len(mock.execCreated) != 2 {
		t.Fatalf("expected 2 exec creates, got %d", len(mock.execCreated))
	}

	// First exec: install.sh with env vars.
	installExec := mock.execCreated[0]
	if installExec.User != "0:0" {
		t.Errorf("exec User = %q, want 0:0", installExec.User)
	}
	wantCmd := "bash"
	if len(installExec.Cmd) < 1 || installExec.Cmd[0] != wantCmd {
		t.Errorf("exec Cmd[0] = %q, want %q", installExec.Cmd[0], wantCmd)
	}
	if !strings.Contains(installExec.Cmd[1], "python/install.sh") {
		t.Errorf("exec Cmd[1] = %q, want path containing python/install.sh", installExec.Cmd[1])
	}
}

func TestInstallFeature_TarContainsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "helper.sh"), []byte("helper"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{exitCode: 0}
	inst := NewInstaller(mock, testLogger())

	feature := &ResolvedFeature{
		Ref: "ghcr.io/example/feature:1",
		Dir: dir,
		Metadata: FeatureMetadata{
			ID: "myfeature",
		},
	}

	if err := inst.InstallFeature(context.Background(), "ctr", feature, nil); err != nil {
		t.Fatal(err)
	}

	// Extract the tar that was copied and verify contents.
	if len(mock.copiedTars) == 0 {
		t.Fatal("no tar copied")
	}

	tr := tar.NewReader(bytes.NewReader(mock.copiedTars[0].data))
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}

	// Normalize directory entries: some tar implementations omit trailing slashes.
	for i, n := range names {
		if !strings.HasSuffix(n, "/") {
			// Check if it was a directory by seeing if other entries are under it.
			for _, other := range names {
				if strings.HasPrefix(other, n+"/") {
					names[i] = n + "/"
					break
				}
			}
		}
	}
	sort.Strings(names)
	wantEntries := []string{"myfeature/", "myfeature/install.sh", "myfeature/scripts/", "myfeature/scripts/helper.sh"}
	sort.Strings(wantEntries)

	if len(names) != len(wantEntries) {
		t.Fatalf("tar entries = %v, want %v", names, wantEntries)
	}
	for i, name := range names {
		if name != wantEntries[i] {
			t.Errorf("tar entry[%d] = %q, want %q", i, name, wantEntries[i])
		}
	}
}

func TestInstallFeature_EnvVars(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash"), 0o755); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{exitCode: 0}
	inst := NewInstaller(mock, testLogger())

	feature := &ResolvedFeature{
		Ref: "ghcr.io/example/node:1",
		Dir: dir,
		Metadata: FeatureMetadata{
			ID: "node",
		},
	}

	options := map[string]any{
		"version":    "20",
		"nodeGypDep": true,
	}

	if err := inst.InstallFeature(context.Background(), "ctr", feature, options); err != nil {
		t.Fatal(err)
	}

	if len(mock.execCreated) == 0 {
		t.Fatal("no exec calls")
	}

	env := mock.execCreated[0].Env
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["_CONTAINER_ID"] != "node" {
		t.Errorf("_CONTAINER_ID = %q, want node", envMap["_CONTAINER_ID"])
	}
	if envMap["_REMOTE_USER"] != "agent" {
		t.Errorf("_REMOTE_USER = %q, want agent", envMap["_REMOTE_USER"])
	}
	if envMap["VERSION"] != "20" {
		t.Errorf("VERSION = %q, want 20", envMap["VERSION"])
	}
	if envMap["NODEGYPDEP"] != "true" {
		t.Errorf("NODEGYPDEP = %q, want true", envMap["NODEGYPDEP"])
	}
}

func TestInstallFeature_ExecCreateError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash"), 0o755); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		execCreateErr: fmt.Errorf("docker daemon unavailable"),
	}
	inst := NewInstaller(mock, testLogger())

	feature := &ResolvedFeature{
		Ref: "ghcr.io/example/python:1",
		Dir: dir,
		Metadata: FeatureMetadata{
			ID: "python",
		},
	}

	err := inst.InstallFeature(context.Background(), "ctr", feature, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "executing install.sh") {
		t.Errorf("error = %q, want it to contain 'executing install.sh'", err)
	}
}

func TestInstallFeature_NonZeroExitCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash\nexit 1"), 0o755); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		exitCode:   1,
		execOutput: "E: Unable to locate package foo",
	}
	inst := NewInstaller(mock, testLogger())

	feature := &ResolvedFeature{
		Ref: "ghcr.io/example/fail:1",
		Dir: dir,
		Metadata: FeatureMetadata{
			ID: "fail",
		},
	}

	err := inst.InstallFeature(context.Background(), "ctr", feature, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "exited with code 1") {
		t.Errorf("error = %q, want it to contain 'exited with code 1'", err)
	}
}

func TestInstallFeature_CopyError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash"), 0o755); err != nil {
		t.Fatal(err)
	}

	mock := &mockDockerClient{
		copyErr: fmt.Errorf("container not found"),
	}
	inst := NewInstaller(mock, testLogger())

	feature := &ResolvedFeature{
		Ref: "ghcr.io/example/python:1",
		Dir: dir,
		Metadata: FeatureMetadata{
			ID: "python",
		},
	}

	err := inst.InstallFeature(context.Background(), "ctr", feature, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "copying feature python") {
		t.Errorf("error = %q, want it to contain 'copying feature python'", err)
	}
}

func TestBuildFeatureEnv(t *testing.T) {
	env := buildFeatureEnv("python", map[string]any{
		"version": "3.11",
		"tools":   "pip",
	})

	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["_CONTAINER_ID"] != "python" {
		t.Errorf("_CONTAINER_ID = %q", envMap["_CONTAINER_ID"])
	}
	if envMap["_REMOTE_USER"] != "agent" {
		t.Errorf("_REMOTE_USER = %q", envMap["_REMOTE_USER"])
	}
	if envMap["VERSION"] != "3.11" {
		t.Errorf("VERSION = %q", envMap["VERSION"])
	}
	if envMap["TOOLS"] != "pip" {
		t.Errorf("TOOLS = %q", envMap["TOOLS"])
	}
}

func TestBuildFeatureEnv_NilOptions(t *testing.T) {
	env := buildFeatureEnv("node", nil)
	if len(env) != 2 {
		t.Errorf("expected 2 env vars (_CONTAINER_ID, _REMOTE_USER), got %d", len(env))
	}
}

func TestCreateTarFromDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash\necho hi"), 0o755); err != nil {
		t.Fatal(err)
	}

	buf, err := createTarFromDir(dir, "testfeature")
	if err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(buf)
	var entries []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, hdr.Name)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 tar entries, got %d: %v", len(entries), entries)
	}
	if entries[0] != "testfeature/" {
		t.Errorf("entry[0] = %q, want testfeature/", entries[0])
	}
	if entries[1] != "testfeature/install.sh" {
		t.Errorf("entry[1] = %q, want testfeature/install.sh", entries[1])
	}
}
