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

	// 1. Create tar archive of the feature directory.
	tarBuf, err := createTarFromDir(feature.Dir, featureID)
	if err != nil {
		return fmt.Errorf("creating tar for feature %s: %w", featureID, err)
	}

	// 2. Copy into container.
	if err := inst.docker.CopyToContainer(installCtx, containerID, destBase, tarBuf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copying feature %s to container: %w", featureID, err)
	}

	// 3. Build environment variables from options.
	env := buildFeatureEnv(containerID, featureID, options)

	// 4. Execute install.sh as root.
	installScript := destDir + "/install.sh"
	output, exitCode, err := inst.execInContainer(installCtx, containerID, []string{"bash", installScript}, env)
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

// execInContainerAsUser runs a command inside the container as the given user.
// This matches the ExecFunc signature needed by the mise integration.
func (inst *Installer) execInContainerAsUser(ctx context.Context, containerID string, cmd []string, user string, env []string) (string, int, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		User:         user,
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
func buildFeatureEnv(containerID, featureID string, options map[string]any) []string {
	env := []string{
		"_CONTAINER_ID=" + containerID,
		"_REMOTE_USER=agent",
	}

	for key, val := range options {
		envKey := strings.ToUpper(key)
		envVal := fmt.Sprintf("%v", val)
		env = append(env, envKey+"="+envVal)
	}

	return env
}
