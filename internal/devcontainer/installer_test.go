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
	// execCreateErr / exitCode / execOutput apply to the install.sh call
	// (the 2nd exec — the 1st is always a mkdir for destBase and returns OK).
	// Use execCreateErrAny for tests that need every exec to fail.
	execCreateErr    error
	execCreateErrAny bool
	execAttachErr    error
	execInspectErr   error
	copyErr          error
	exitCode         int
	execOutput       string
}

type copiedTar struct {
	containerID string
	dstPath     string
	data        []byte
}

func (m *mockDockerClient) ContainerExecCreate(_ context.Context, _ string, config container.ExecOptions) (container.ExecCreateResponse, error) {
	m.execCreated = append(m.execCreated, config)
	// Error only on the install.sh call (index 1) unless execCreateErrAny.
	if m.execCreateErr != nil && (m.execCreateErrAny || len(m.execCreated) == 2) {
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
	// Mkdir (1st exec) always returns 0; non-zero exitCode applies to install.sh.
	if len(m.execCreated) == 1 {
		return container.ExecInspect{ExitCode: 0}, nil
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

	// Verify exec calls: mkdir destBase + install.sh + rm -rf cleanup = 3 exec creates.
	if len(mock.execCreated) != 3 {
		t.Fatalf("expected 3 exec creates, got %d", len(mock.execCreated))
	}

	// [0] = mkdir destBase, [1] = install.sh, [2] = rm cleanup.
	installExec := mock.execCreated[1]
	if installExec.User != "0:0" {
		t.Errorf("exec User = %q, want 0:0", installExec.User)
	}
	wantCmd := "bash"
	if len(installExec.Cmd) < 3 || installExec.Cmd[0] != wantCmd {
		t.Errorf("exec Cmd[0] = %q, want %q", installExec.Cmd[0], wantCmd)
	}
	if installExec.Cmd[1] != "-e" {
		t.Errorf("exec Cmd[1] = %q, want -e (strict mode)", installExec.Cmd[1])
	}
	if !strings.Contains(installExec.Cmd[2], "python/install.sh") {
		t.Errorf("exec Cmd[2] = %q, want path containing python/install.sh", installExec.Cmd[2])
	}

	// install.sh must run with the feature directory as CWD so that relative
	// paths inside the script (e.g., `./scripts/vendor/...`) resolve against
	// the feature's own files. Regression guard for the aws-cli feature
	// which failed with "cp: cannot stat './scripts/vendor/aws_bash_completer'"
	// when WorkingDir was not set.
	wantWorkDir := "/tmp/devcontainer-features/python"
	if installExec.WorkingDir != wantWorkDir {
		t.Errorf("install.sh exec WorkingDir = %q, want %q", installExec.WorkingDir, wantWorkDir)
	}
	// mkdir and rm run without a WorkingDir override (absolute paths).
	if mock.execCreated[0].WorkingDir != "" {
		t.Errorf("mkdir exec WorkingDir = %q, want empty", mock.execCreated[0].WorkingDir)
	}
	if mock.execCreated[2].WorkingDir != "" {
		t.Errorf("rm exec WorkingDir = %q, want empty", mock.execCreated[2].WorkingDir)
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

	if len(mock.execCreated) < 2 {
		t.Fatalf("expected at least 2 exec calls (mkdir + install.sh), got %d", len(mock.execCreated))
	}

	// [0] = mkdir destBase; [1] = install.sh with feature env vars.
	env := mock.execCreated[1].Env
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["_CONTAINER_ID"] != "ctr" {
		t.Errorf("_CONTAINER_ID = %q, want ctr", envMap["_CONTAINER_ID"])
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
	env := buildFeatureEnv("container-abc", "python", nil, map[string]any{
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

	if envMap["_CONTAINER_ID"] != "container-abc" {
		t.Errorf("_CONTAINER_ID = %q, want container-abc", envMap["_CONTAINER_ID"])
	}
	if envMap["_REMOTE_USER"] != "agent" {
		t.Errorf("_REMOTE_USER = %q", envMap["_REMOTE_USER"])
	}
	if envMap["_REMOTE_USER_HOME"] != "/home/agent" {
		t.Errorf("_REMOTE_USER_HOME = %q, want /home/agent", envMap["_REMOTE_USER_HOME"])
	}
	if envMap["_CONTAINER_USER"] != "root" {
		t.Errorf("_CONTAINER_USER = %q, want root", envMap["_CONTAINER_USER"])
	}
	if envMap["_CONTAINER_USER_HOME"] != "/root" {
		t.Errorf("_CONTAINER_USER_HOME = %q, want /root", envMap["_CONTAINER_USER_HOME"])
	}
	if envMap["VERSION"] != "3.11" {
		t.Errorf("VERSION = %q", envMap["VERSION"])
	}
	if envMap["TOOLS"] != "pip" {
		t.Errorf("TOOLS = %q", envMap["TOOLS"])
	}
}

func TestBuildFeatureEnv_NilOptions(t *testing.T) {
	env := buildFeatureEnv("container-xyz", "node", nil, nil)
	// With no options, only the standard devcontainer feature-contract vars
	// are emitted: _CONTAINER_ID, _REMOTE_USER, _REMOTE_USER_HOME,
	// _CONTAINER_USER, _CONTAINER_USER_HOME.
	want := map[string]string{
		"_CONTAINER_ID":        "container-xyz",
		"_REMOTE_USER":         "agent",
		"_REMOTE_USER_HOME":    "/home/agent",
		"_CONTAINER_USER":      "root",
		"_CONTAINER_USER_HOME": "/root",
	}
	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	if len(env) != len(want) {
		t.Errorf("expected %d env vars, got %d: %v", len(want), len(env), env)
	}
	for k, v := range want {
		if envMap[k] != v {
			t.Errorf("%s = %q, want %q", k, envMap[k], v)
		}
	}
}

// Regression test for the provisioning failure where the claude-code feature's
// install.sh does `cp "$_REMOTE_USER_HOME/.local/bin/claude" /usr/local/bin/claude`
// and _REMOTE_USER_HOME was unset, expanding to `/.local/bin/claude` → "cannot stat".
func TestBuildFeatureEnv_SetsRemoteUserHome(t *testing.T) {
	env := buildFeatureEnv("c1", "claude-code", nil, nil)
	var got string
	for _, e := range env {
		if strings.HasPrefix(e, "_REMOTE_USER_HOME=") {
			got = strings.TrimPrefix(e, "_REMOTE_USER_HOME=")
		}
	}
	if got != "/home/agent" {
		t.Fatalf("_REMOTE_USER_HOME = %q, want /home/agent", got)
	}
}

// Regression test: when the user enables a feature with no explicit options
// (the default UI flow — user just toggles a switch), defaults from the
// feature's own devcontainer-feature.json must reach install.sh as env vars.
// Without this, official upstream features like `git` blow up with
// "[: =: unary operator expected" when their install.sh tries `[ "$VERSION" = "latest" ]`.
func TestBuildFeatureEnv_AppliesMetadataDefaultsWhenUserOptionsEmpty(t *testing.T) {
	metadata := map[string]any{
		"version": map[string]any{"type": "string", "default": "latest"},
		"ppa":     map[string]any{"type": "boolean", "default": true},
		// Option without a default — should be omitted, not injected as empty.
		"profile": map[string]any{"type": "string"},
	}
	env := buildFeatureEnv("c1", "git", metadata, map[string]any{})

	envMap := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	if envMap["VERSION"] != "latest" {
		t.Errorf("VERSION default = %q, want latest", envMap["VERSION"])
	}
	if envMap["PPA"] != "true" {
		t.Errorf("PPA default = %q, want true", envMap["PPA"])
	}
	if _, ok := envMap["PROFILE"]; ok {
		t.Errorf("PROFILE should be unset (no default in metadata), got %q", envMap["PROFILE"])
	}
}

// User-provided values must override metadata defaults, even when empty —
// an explicit `"version": ""` in devcontainer.json is a valid (if rare)
// signal that the user wants the empty form, not the metadata default.
func TestBuildFeatureEnv_UserOptionsOverrideDefaults(t *testing.T) {
	metadata := map[string]any{
		"version": map[string]any{"type": "string", "default": "latest"},
	}
	env := buildFeatureEnv("c1", "git", metadata, map[string]any{
		"version": "system",
	})

	// Last KEY=VALUE wins per docker exec semantics, so VERSION=system must
	// appear after any default. We assert both presence and ordering.
	var defaultIdx, userIdx int = -1, -1
	for i, e := range env {
		if e == "VERSION=latest" {
			defaultIdx = i
		}
		if e == "VERSION=system" {
			userIdx = i
		}
	}
	if userIdx < 0 {
		t.Fatalf("expected VERSION=system in env, got %v", env)
	}
	if defaultIdx >= 0 && defaultIdx > userIdx {
		t.Errorf("default VERSION=latest at idx %d came after user VERSION=system at idx %d", defaultIdx, userIdx)
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
