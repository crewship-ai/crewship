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
	"github.com/docker/docker/pkg/stdcopy"
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

	// The exec is attached WITHOUT a TTY (ExecOptions has no Tty:true), so
	// Docker multiplexes stdout/stderr with an 8-byte stdcopy frame header per
	// write ([STREAM_TYPE,0,0,0,SIZE...]). A raw read leaves those headers in the
	// output — harmless for callers that only check the exit code, but
	// captureLoginPath uses the value VERBATIM as the container PATH, so the
	// leading "\x01\x00\x00\x00\x00\x00\x00…" header injected a NUL byte and runc
	// rejected every container with `invalid environment variable "PATH":
	// contains nul byte`. Read raw, then demultiplex only when the bytes are
	// actually framed (see demuxDockerStream) — that keeps this correct for a
	// real daemon while staying a no-op for a raw/TTY stream.
	var raw bytes.Buffer
	if _, copyErr := io.Copy(&raw, resp.Reader); copyErr != nil {
		inst.logger.Warn("failed to read exec output", "error", copyErr)
	}
	buf := bytes.NewBuffer(demuxDockerStream(raw.Bytes()))

	inspect, err := inst.docker.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return buf.String(), -1, fmt.Errorf("exec inspect: %w", err)
	}

	return buf.String(), inspect.ExitCode, nil
}

// demuxDockerStream returns the payload of a Docker exec attach stream. A
// non-TTY exec is multiplexed with 8-byte stdcopy frame headers
// ([STREAM_TYPE(0..2),0,0,0,SIZE(4 BE)]); a TTY exec — or a raw-stream test
// double — is not. We detect framing from the first header and demux only when
// it's present, so a raw stream passes through untouched. stdout+stderr collapse
// into one buffer, matching the previous combined-output behaviour.
func demuxDockerStream(b []byte) []byte {
	if !looksMultiplexed(b) {
		return b
	}
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, bytes.NewReader(b)); err != nil {
		// Malformed framing: fall back to raw bytes rather than dropping output.
		return b
	}
	return out.Bytes()
}

// looksMultiplexed reports whether b starts with a plausible stdcopy frame
// header: a stream-type byte in {0,1,2} followed by three zero bytes. Real
// command output starts with a printable byte, so it never false-matches.
func looksMultiplexed(b []byte) bool {
	return len(b) >= 8 && b[0] <= 2 && b[1] == 0 && b[2] == 0 && b[3] == 0
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

// Feature-install-contract identity, per https://containers.dev/implementors/features/.
// The remote user is the non-root user common-utils creates (UID 1001); the
// container user is root (features install as root at build/exec time).
const (
	featureRemoteUser        = "agent"
	featureRemoteUserHome    = "/home/agent"
	featureContainerUser     = "root"
	featureContainerUserHome = "/root"
)

// featureContractEnv returns the standard devcontainer feature-install-contract
// variables that the runner MUST supply, shared by BOTH install paths:
// exec-install (buildFeatureEnv) and the BuildKit Dockerfile RUN (dockerfile.go).
// Keeping a single source prevents the two paths from drifting — a drift that
// previously left _REMOTE_USER_HOME unset on the BuildKit path and broke every
// feature whose install.sh copies out of $_REMOTE_USER_HOME. _CONTAINER_ID is
// path-specific (a runtime container id) and added by callers that have one.
func featureContractEnv() []string {
	return []string{
		"_REMOTE_USER=" + featureRemoteUser,
		"_REMOTE_USER_HOME=" + featureRemoteUserHome,
		"_CONTAINER_USER=" + featureContainerUser,
		"_CONTAINER_USER_HOME=" + featureContainerUserHome,
	}
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
	// Standard devcontainer feature-contract variables the runner MUST supply
	// (https://containers.dev/implementors/features/). _REMOTE_USER is the
	// non-root user common-utils creates (UID 1001, home /home/agent);
	// _CONTAINER_USER is the user the build-time exec runs as (root). Upstream
	// features rely on these — e.g. a feature that installs a tool as
	// $_REMOTE_USER then promotes it to a system path via
	// `cp "$_REMOTE_USER_HOME/.local/bin/<tool>" ...` silently became
	// `cp /.local/bin/<tool>` (failing the whole build) when _REMOTE_USER_HOME
	// was unset. featureContractEnv is shared with the BuildKit Dockerfile path
	// (dockerfile.go) so the two install paths can never drift apart again.
	env := append([]string{"_CONTAINER_ID=" + containerID}, featureContractEnv()...)

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
