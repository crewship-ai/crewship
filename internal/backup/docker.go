package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerOps abstracts the Docker operations the backup / restore flow
// needs. It exists so the runner can be unit-tested without spinning
// up a real daemon. The sole production implementation is
// MobyDockerOps; tests substitute an in-memory fake.
type DockerOps interface {
	// Pause suspends the container's processes so tar / CopyFromContainer
	// sees a stable filesystem. Returns nil if the container is already
	// paused (idempotent for crash-safe backup flows).
	Pause(ctx context.Context, containerID string) error

	// Unpause resumes a previously paused container. Safe to call on a
	// container that is already running.
	Unpause(ctx context.Context, containerID string) error

	// CopyFrom streams the contents of srcPath inside the container as a
	// tar archive. The caller owns the returned ReadCloser.
	CopyFrom(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error)

	// CopyTo writes the given tar-encoded content into dstPath inside the
	// container. The container must exist (created or running); dstPath
	// must already exist — docker does not create parent directories.
	CopyTo(ctx context.Context, containerID, dstPath string, content io.Reader) error

	// ContainerExists reports whether a container with the given ID or
	// name is known to the daemon. Used by restore preflight so CopyTo
	// does not fail with a cryptic "No such container" mid-stream.
	ContainerExists(ctx context.Context, containerID string) (bool, error)

	// Exec runs a single command inside the container as root, collects
	// its combined stdout+stderr, and returns the exit code. Used by the
	// backup self-test to destroy a canary file between collect and
	// restore. Not performance-critical; the implementation blocks on the
	// command finishing.
	Exec(ctx context.Context, containerID string, cmd []string) (exitCode int, output []byte, err error)
}

// MobyDockerOps is the production implementation backed by the moby
// client library. Callers should pass the *client.Client already held
// by the Docker provider rather than constructing a new one, so that
// Docker socket / TLS configuration stays consistent.
type MobyDockerOps struct {
	Client *client.Client
}

// Pause implements DockerOps.
func (m *MobyDockerOps) Pause(ctx context.Context, containerID string) error {
	if err := m.Client.ContainerPause(ctx, containerID); err != nil {
		// Docker returns "is already paused" with varying wording; we
		// treat that as success so a retried backup does not double-fail.
		if strings.Contains(err.Error(), "already paused") {
			return nil
		}
		return fmt.Errorf("backup: docker pause %s: %w", containerID, err)
	}
	return nil
}

// Unpause implements DockerOps.
func (m *MobyDockerOps) Unpause(ctx context.Context, containerID string) error {
	if err := m.Client.ContainerUnpause(ctx, containerID); err != nil {
		if strings.Contains(err.Error(), "is not paused") {
			return nil
		}
		return fmt.Errorf("backup: docker unpause %s: %w", containerID, err)
	}
	return nil
}

// CopyFrom implements DockerOps.
func (m *MobyDockerOps) CopyFrom(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error) {
	r, _, err := m.Client.CopyFromContainer(ctx, containerID, srcPath)
	if err != nil {
		return nil, fmt.Errorf("backup: docker cp from %s:%s: %w", containerID, srcPath, err)
	}
	return r, nil
}

// ContainerExists implements DockerOps by probing the daemon. Any
// error containing "No such container" resolves to (false, nil);
// other errors (daemon unreachable, permission denied) bubble up.
func (m *MobyDockerOps) ContainerExists(ctx context.Context, containerID string) (bool, error) {
	_, err := m.Client.ContainerInspect(ctx, containerID)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "No such container") ||
		strings.Contains(err.Error(), "not found") {
		return false, nil
	}
	return false, fmt.Errorf("backup: docker inspect %s: %w", containerID, err)
}

// CopyTo implements DockerOps.
func (m *MobyDockerOps) CopyTo(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	if err := m.Client.CopyToContainer(ctx, containerID, dstPath, content, container.CopyToContainerOptions{
		AllowOverwriteDirWithFile: false,
		CopyUIDGID:                true,
	}); err != nil {
		return fmt.Errorf("backup: docker cp to %s:%s: %w", containerID, dstPath, err)
	}
	return nil
}

// Exec implements DockerOps. Runs cmd as root (0:0) with stdout and stderr
// attached so the caller gets a single combined buffer back — matches the
// semantics of exec_test patterns elsewhere in the codebase (see
// internal/devcontainer/installer.go:execInContainerFull).
func (m *MobyDockerOps) Exec(ctx context.Context, containerID string, cmd []string) (int, []byte, error) {
	exec, err := m.Client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		User:         "0:0",
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return -1, nil, fmt.Errorf("backup: exec create %s: %w", containerID, err)
	}
	resp, err := m.Client.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return -1, nil, fmt.Errorf("backup: exec attach %s: %w", containerID, err)
	}
	defer resp.Close()

	// ContainerExecAttach returns a multiplexed stream when Tty is false
	// (our default): each chunk is prefixed with an 8-byte header
	// (stream type + big-endian length). Using io.Copy here would smuggle
	// those bytes into the caller's buffer. stdcopy.StdCopy parses the
	// framing and de-interleaves stdout and stderr correctly.
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, resp.Reader); err != nil {
		// Best-effort read; still try to report the exit code below.
		_ = err
	}
	var buf bytes.Buffer
	buf.Write(stdout.Bytes())
	buf.Write(stderr.Bytes())
	inspect, err := m.Client.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return -1, buf.Bytes(), fmt.Errorf("backup: exec inspect %s: %w", containerID, err)
	}
	return inspect.ExitCode, buf.Bytes(), nil
}

// ErrPauseUnpauseLost is returned by WithPaused when unpause fails
// after a successful tar. Callers should log it loudly — the container
// remains paused and a human operator must intervene. The backup
// itself is still considered complete.
var ErrPauseUnpauseLost = errors.New("backup: container left paused; manual unpause required")

// WithPaused runs fn while the given container is paused, unpausing
// afterwards regardless of fn's outcome. If unpause fails, the inner
// error is returned if any; otherwise ErrPauseUnpauseLost wraps the
// unpause error so callers can alert an operator.
func WithPaused(ctx context.Context, ops DockerOps, containerID string, fn func() error) (retErr error) {
	if err := ops.Pause(ctx, containerID); err != nil {
		return err
	}
	defer func() {
		if err := ops.Unpause(ctx, containerID); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("%w: %v", ErrPauseUnpauseLost, err)
			}
		}
	}()
	return fn()
}

// RepackTar reads a tar stream from src (typically from CopyFrom) and
// writes each entry to dst using the TarZstWriter. Entry names are
// rewritten to live under prefix (e.g. "home/" so the final bundle
// keeps sections separate). Returns the total bytes written to dst.
func RepackTar(src io.Reader, dst *TarZstWriter, prefix string) (int64, error) {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	tr := tar.NewReader(src)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("backup: repack tar: %w", err)
		}
		newName := prefix + strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag != tar.TypeReg {
			// Non-regular entries (dirs, symlinks) pass through with an
			// empty body; rely on the outer writer for regular-file
			// framing but preserve metadata for dirs/symlinks too.
			if err := dst.tw.WriteHeader(&tar.Header{
				Name:     newName,
				Mode:     hdr.Mode,
				ModTime:  hdr.ModTime,
				Typeflag: hdr.Typeflag,
				Linkname: hdr.Linkname,
				Uid:      hdr.Uid,
				Gid:      hdr.Gid,
			}); err != nil {
				return total, fmt.Errorf("backup: repack header %q: %w", newName, err)
			}
			continue
		}
		if err := dst.WriteStream(newName, hdr.Mode, hdr.ModTime, hdr.Size, tr); err != nil {
			return total, err
		}
		total += hdr.Size
	}
	return total, nil
}
