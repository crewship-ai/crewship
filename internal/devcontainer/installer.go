package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

var featureIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,49}$`)

// DockerClient is the subset of the Docker API needed for feature installation.
// A real *client.Client satisfies this interface.
type DockerClient interface {
	ContainerExecCreate(ctx context.Context, container string, config container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecStartOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader, options container.CopyToContainerOptions) error
}

// Installer installs devcontainer features into a running container via
// docker exec. Each feature is copied as a tar archive and its install.sh
// is executed as root.
type Installer struct {
	docker DockerClient
	logger *slog.Logger
}

// NewInstaller creates an Installer that uses docker for container operations.
func NewInstaller(docker DockerClient, logger *slog.Logger) *Installer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Installer{
		docker: docker,
		logger: logger,
	}
}

// InstallFeature copies the resolved feature into the container and runs its
// install.sh script as root. User-provided options are passed as uppercase
// environment variables (e.g., option key "version" becomes VERSION=value).
func (inst *Installer) InstallFeature(ctx context.Context, containerID string, feature *ResolvedFeature, options map[string]any) error {
	featureID := feature.Metadata.ID

	// Validate feature ID is safe for filesystem paths.
	if !featureIDRe.MatchString(featureID) {
		return fmt.Errorf("invalid feature ID %q: must be alphanumeric with dots/hyphens/underscores", featureID)
	}

	destBase := "/tmp/devcontainer-features"
	destDir := destBase + "/" + featureID

	// Apply a 30-minute timeout for the entire feature installation.
	installCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	inst.logger.Info("installing feature", "id", featureID, "container", containerID)

	// 1. Pre-create destBase in the container via exec. CopyToContainer
	// requires the target parent directory to exist. Verify exit code.
	output, exitCode, err := inst.execInContainer(installCtx, containerID, []string{"mkdir", "-p", destBase}, nil)
	if err != nil {
		return fmt.Errorf("mkdir %s in container: %w", destBase, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mkdir %s in container: exit code %d: %s", destBase, exitCode, output)
	}
	inst.logger.Debug("destBase created", "path", destBase, "output", output)

	// 2. Create tar archive of the feature directory (no prefix — tar entries
	// are relative paths that Docker extracts into destBase).
	tarBuf, err := createTarFromDir(feature.Dir, featureID)
	if err != nil {
		return fmt.Errorf("creating tar for feature %s: %w", featureID, err)
	}

	// 3. Copy into container at destBase.
	if err := inst.docker.CopyToContainer(installCtx, containerID, destBase, tarBuf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copying feature %s to container: %w", featureID, err)
	}

	// 3. Build environment variables from options + metadata defaults.
	env := buildFeatureEnv(containerID, featureID, feature.Metadata.Options, options)

	// 4. Execute install.sh as root, with the feature directory as CWD so that
	// relative paths inside install.sh (e.g., `./scripts/vendor/...`) resolve
	// against feature-provided resources. `bash -e` stops on first error, so a
	// failing command surfaces as non-zero exit instead of being silently
	// ignored. Scripts that explicitly opt out with `set +e` still work.
	installScript := destDir + "/install.sh"
	output, exitCode, err = inst.execInContainerInDir(installCtx, containerID, destDir, []string{"bash", "-e", installScript}, env)
	if err != nil {
		return fmt.Errorf("executing install.sh for feature %s: %w", featureID, err)
	}

	if output != "" {
		inst.logger.Debug("feature install output", "id", featureID, "output", output)
	}

	if exitCode != 0 {
		return fmt.Errorf("install.sh for feature %s exited with code %d: %s", featureID, exitCode, output)
	}

	// 5. Clean up feature files inside the container.
	_, _, cleanupErr := inst.execInContainer(installCtx, containerID, []string{"rm", "-rf", destDir}, nil)
	if cleanupErr != nil {
		inst.logger.Warn("failed to clean up feature directory", "id", featureID, "error", cleanupErr)
	}

	inst.logger.Info("feature installed", "id", featureID)
	return nil
}

// execInContainer runs a command inside the container as root (0:0) and returns
// the combined stdout+stderr output, exit code, and any error.
func (inst *Installer) execInContainer(ctx context.Context, containerID string, cmd, env []string) (string, int, error) {
	return inst.execInContainerAsUser(ctx, containerID, cmd, "0:0", env)
}

// execInContainerInDir runs a command inside the container as root (0:0) with
// the given working directory. Used for feature install.sh so relative paths
// inside the script resolve against the feature's own directory layout.
func (inst *Installer) execInContainerInDir(ctx context.Context, containerID, workDir string, cmd, env []string) (string, int, error) {
	return inst.execInContainerFull(ctx, containerID, cmd, "0:0", workDir, env)
}

// execInContainerAsUser runs a command inside the container as the given user.
// This matches the ExecFunc signature needed by the mise integration.
func (inst *Installer) execInContainerAsUser(ctx context.Context, containerID string, cmd []string, user string, env []string) (string, int, error) {
	return inst.execInContainerFull(ctx, containerID, cmd, user, "", env)
}

// execInContainerFull is the single underlying exec path. workDir is optional —
// when empty, no WorkingDir override is applied and the container's default
// WORKDIR is used.
func (inst *Installer) execInContainerFull(ctx context.Context, containerID string, cmd []string, user, workDir string, env []string) (string, int, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		User:         user,
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
	}

	exec, err := inst.docker.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", -1, fmt.Errorf("exec create: %w", err)
	}

	resp, err := inst.docker.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return "", -1, fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, resp.Reader); copyErr != nil {
		inst.logger.Warn("failed to read exec output", "error", copyErr)
	}

	inspect, err := inst.docker.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return buf.String(), -1, fmt.Errorf("exec inspect: %w", err)
	}

	return buf.String(), inspect.ExitCode, nil
}

// createTarFromDir creates a tar archive of the directory at srcDir. The
// archive entries are placed under the given prefix directory so that when
// extracted at a base path, the files land in {base}/{prefix}/.
func createTarFromDir(srcDir, prefix string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Add prefix directory entry.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     prefix + "/",
		Mode:     0o755,
	}); err != nil {
		return nil, err
	}

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		// Compute relative path within the feature directory.
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // skip the root directory itself
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = prefix + "/" + filepath.ToSlash(rel)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		f.Close()
		return copyErr
	})
	if err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}

	return &buf, nil
}

// buildFeatureEnv constructs environment variables for a feature installation.
// Each option key is uppercased (e.g., "version" -> "VERSION=3.11").
// Standard devcontainer variables _CONTAINER_ID and _REMOTE_USER are included.
// containerID is the actual Docker container ID per the devcontainer spec.
//
// Defaults from the feature's own metadata (devcontainer-feature.json
// `options.<name>.default`) are applied when the user didn't explicitly set
// a value. Per https://containers.dev/implementors/features/#options-property,
// install.sh expects every declared option as an env var — most upstream
// scripts assume the default is filled in by the runner and break in subtle
// ways when the variable is empty (the canonical example: the official `git`
// feature's `[ "$VERSION" = "latest" ]` line erroring with "unary operator
// expected" when VERSION is unset).
//
// User-provided values always win over defaults, including empty strings —
// "user explicitly set version=”" is a valid (if rare) intent we don't
// override.
func buildFeatureEnv(containerID, featureID string, metadataOptions, userOptions map[string]any) []string {
	env := []string{
		"_CONTAINER_ID=" + containerID,
		"_REMOTE_USER=agent",
	}

	// Apply defaults from feature metadata for options the user didn't set.
	for key, spec := range metadataOptions {
		if _, hasUserValue := userOptions[key]; hasUserValue {
			continue
		}
		specMap, ok := spec.(map[string]any)
		if !ok {
			continue
		}
		defVal, hasDefault := specMap["default"]
		if !hasDefault {
			continue
		}
		env = append(env, strings.ToUpper(key)+"="+fmt.Sprintf("%v", defVal))
	}

	// User-provided options. Listed second so they override any default
	// already in the slice (Docker exec uses the LAST value for duplicate
	// keys), and visible in the same form spec.io/.devcontainer/.json
	// authors expect: KEY=VALUE.
	for key, val := range userOptions {
		env = append(env, strings.ToUpper(key)+"="+fmt.Sprintf("%v", val))
	}

	return env
}
