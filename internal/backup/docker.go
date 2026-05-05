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

	// CopyToVolume extracts a tar stream into a destination INSIDE the
	// container by piping it through `tar -x` over an exec session. Used
	// for destinations that Docker's CopyToContainer rejects with
	// "container rootfs is marked read-only" or "Could not find the
	// file <path>" — typically named-volume mountpoints (/home/agent,
	// /opt/crew-tools) where Docker's archive-API checks the rootfs
	// layer rather than the live mount table. tar must be on PATH
	// inside the container; devcontainer base images ship it.
	CopyToVolume(ctx context.Context, containerID, dstPath string, content io.Reader) error

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

// CopyToVolume implements DockerOps. Uses an exec session running
// `tar -xf - -C <dst>` with the tar stream attached to stdin. This
// path-resolves through the container's live mount table (so named
// volumes like /home/agent are visible) and bypasses Docker's
// archive-API rootfs check entirely.
//
// Concurrency note: stdout/stderr MUST be drained concurrently with
// stdin pumping. Sequential drain (write all → close → drain) deadlocks
// once the daemon's hijacked-conn output buffer fills, because the
// daemon stops reading our stdin until we read its stdout. Multi-GB
// volumes (mise + pyenv + node_modules) hit this trivially. Verified
// on dev3: a 477 MiB bundle hung tar with stdin blocked at ~1 MB
// transferred. The goroutine here pumps output continuously so back
// pressure flows correctly.
func (m *MobyDockerOps) CopyToVolume(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	// Tar flags rationale (verified against dev3 GNU tar 1.34 inside
	// devcontainer base image):
	//   --overwrite         replace existing files outright. Critical
	//                       to NOT pair with --recursive-unlink: that
	//                       flag deletes parent dirs along with their
	//                       contents, which clobbers anything the
	//                       restore-side excludes (e.g. node_modules
	//                       under .local/lib/) since the parent dir
	//                       header still ships in the bundle.
	//   --no-same-owner     don't chown — running as the agent user
	//                       (uid 1001) we can't chown to other uids
	//                       and don't want to preserve archive uids
	//                       across restored crew identities anyway
	//   --no-same-permissions   trust destination's defaults; avoids
	//                           EPERM on filesystems that refuse mode
	//                           changes
	//   --touch             don't restore mtimes (utime would fail on
	//                       volume root with EPERM)
	//
	// Runs as the AGENT user (1001:1001) rather than root because the
	// volume's existing files were created by agent and root inside
	// the container often lacks write to user-owned files due to the
	// filesystem driver's uid remapping. Tar fails open with
	// "Permission denied" when root tries to overwrite an
	// agent-owned file under those conditions.
	exec, err := m.Client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{
			"tar", "-x",
			"--overwrite",
			"--no-same-owner", "--no-same-permissions", "--touch",
			"-f", "-", "-C", dstPath,
		},
		User:         "1001:1001",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("backup: exec-tar create %s:%s: %w", containerID, dstPath, err)
	}
	resp, err := m.Client.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("backup: exec-tar attach %s:%s: %w", containerID, dstPath, err)
	}
	defer resp.Close()

	// Drain stdout/stderr concurrently so the daemon's output buffer
	// can never fill and back-pressure the input pump. Buffer captured
	// for inclusion in any non-zero-exit error message.
	type drainResult struct {
		out []byte
		err error
	}
	drainCh := make(chan drainResult, 1)
	go func() {
		var combined bytes.Buffer
		_, err := stdcopy.StdCopy(&combined, &combined, resp.Reader)
		drainCh <- drainResult{out: combined.Bytes(), err: err}
	}()

	// Pump the tar stream into the exec's stdin; CloseWrite so tar
	// sees EOF and exits.
	pumpErr := func() error {
		if _, err := io.Copy(resp.Conn, content); err != nil {
			return fmt.Errorf("backup: exec-tar stdin %s:%s: %w", containerID, dstPath, err)
		}
		if err := resp.CloseWrite(); err != nil {
			return fmt.Errorf("backup: exec-tar close-write %s:%s: %w", containerID, dstPath, err)
		}
		return nil
	}()

	// Wait for the drain goroutine even if pump failed — the deferred
	// resp.Close would otherwise race with StdCopy and produce
	// confusing errors.
	drained := <-drainCh
	if pumpErr != nil {
		return pumpErr
	}
	if drained.err != nil && !errors.Is(drained.err, io.EOF) {
		return fmt.Errorf("backup: exec-tar drain %s:%s: %w", containerID, dstPath, drained.err)
	}

	insp, err := m.Client.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return fmt.Errorf("backup: exec-tar inspect %s:%s: %w", containerID, dstPath, err)
	}
	if insp.ExitCode != 0 {
		return fmt.Errorf("backup: exec-tar to %s:%s exited %d: %s", containerID, dstPath, insp.ExitCode, strings.TrimSpace(string(drained.out)))
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

// volumeExclusions lists path patterns we DON'T back up because the
// content is regeneratable cache that bloats the bundle without
// adding restore value. Sized against dev3: /home/agent grew to
// 1.6 GB after one provisioning cycle (mise tools + node_modules +
// pyenv + cursor-agent installer); excluding these brings the bundle
// from ~480 MiB to ~5-10 MiB while preserving everything an operator
// would actually want restored (workspace files, output/memory, agent
// configs, shell rc files, ssh/credentials).
//
// Patterns match against the wrapper-stripped path (i.e. "agent/" is
// already gone for /home/agent entries). Wildcards: a trailing "/"
// means "the dir AND everything under it". Bare strings match
// path components anywhere in the path so node_modules deep in a
// project tree is also caught.
var volumeExclusions = []string{
	// /home/agent caches + tool installations
	".cache/",
	".local/lib/",          // node_modules + python site-packages
	".local/share/mise/",   // mise tool installations (re-fetchable)
	".local/share/cursor-agent/",
	".local/share/pnpm/",
	".local/share/yarn/",
	".local/state/",        // logs/state we don't need
	".npm/",
	".yarn/cache/",
	// Anywhere in the tree
	"node_modules/",
	"__pycache__/",
	".pytest_cache/",
	".mypy_cache/",
	".ruff_cache/",
}

// varLibExclusions filters /var/lib content. Most of /var/lib is the
// package manager's own state (dpkg, apt) — fully reproducible from
// the cached devcontainer image and useless to ship in every bundle.
// What we WANT to keep is per-service data dirs like /var/lib/redis,
// /var/lib/postgresql, /var/lib/mysql that the agent populated at
// runtime; those are NOT in the image and not regeneratable from it.
//
// dev3 baseline (stock devcontainer image, no service installed):
// /var/lib total ~15 MiB; the bulk is /var/lib/dpkg (~10 MiB) and
// /var/lib/apt (~3 MiB). Excluding both shrinks the contribution of
// this section to <1 MiB until a real service writes data here.
var varLibExclusions = []string{
	"dpkg/",
	"apt/",
	"systemd/",
	"polkit-1/",
	// Logs and rotating state — not data we want to restore even if
	// a service wrote them, since they describe the OLD container's
	// runtime, not the bundle's logical contents.
	"logrotate/",
	"private/", // systemd-private session dirs
}

// shouldExcludeFromBundle reports whether a path inside a volume
// section should be skipped. Conservative — only excludes paths that
// match one of the explicit patterns above so an operator can audit
// the list and add their own. Path is always wrapper-stripped (e.g.
// ".cache/mise/foo") not raw.
func shouldExcludeFromBundle(p string) bool {
	return shouldExclude(p, volumeExclusions)
}

// shouldExclude is the section-aware version: callers from CollectCrew
// pick the right exclusion list (volumeExclusions for /workspace and
// the named volumes, varLibExclusions for /var/lib).
func shouldExclude(p string, patterns []string) bool {
	for _, pat := range patterns {
		if strings.HasSuffix(pat, "/") {
			// Directory pattern: match exact dir or any descendant
			needle := strings.TrimSuffix(pat, "/")
			if p == needle || strings.HasPrefix(p, pat) {
				return true
			}
			// Also match the same dir name nested anywhere (e.g.
			// node_modules under a project subdir)
			if strings.Contains(p, "/"+pat) || strings.Contains(p, "/"+needle+"/") {
				return true
			}
		} else if p == pat || strings.Contains(p, "/"+pat) {
			return true
		}
	}
	return false
}

// RepackTar reads a tar stream from src (typically from CopyFrom) and
// writes each entry to dst using the TarZstWriter. Entry names are
// rewritten to live under prefix (e.g. "home/" so the final bundle
// keeps sections separate).
//
// Strips the wrapper directory that Docker's CopyFromContainer adds
// to the top of its output: a CopyFrom("/workspace") returns a tar
// whose entries start with "workspace/<contents>". Without stripping,
// the bundle layout doubles up — workspace/<slug>/workspace/ — and
// restore lands files at /workspace/workspace/<file> instead of
// /workspace/<file>. Each backup-restore cycle would otherwise nest
// the data one level deeper (reproduced on dev3: /workspace/
// workspace/workspace/workspace/... after three restores).
//
// Wrapper detection: the first directory entry whose name has no
// internal slash is treated as the wrapper. Subsequent entries are
// stripped of that prefix. If the input tar uses the alternate
// "./<file>" layout (used by some test fixtures), no wrapper is
// detected and entries are kept under prefix as-is.
//
// Filters out entries matching volumeExclusions (regeneratable caches:
// node_modules, pyenv, mise installations, etc.) — the size win is
// dramatic (1.6 GB → ~50 MB on dev3) and the excluded content can be
// re-fetched by the agent's normal startup.
//
// Returns the total bytes written to dst.
func RepackTar(src io.Reader, dst *TarZstWriter, prefix string) (int64, error) {
	return RepackTarWithExcludes(src, dst, prefix, volumeExclusions)
}

// RepackTarWithExcludes is RepackTar with an explicit exclusion list,
// so /var/lib (where dpkg/apt state should be skipped, NOT
// node_modules) and /workspace (the opposite) can both be repacked
// through one code path.
func RepackTarWithExcludes(src io.Reader, dst *TarZstWriter, prefix string, excludes []string) (int64, error) {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	tr := tar.NewReader(src)
	var total int64
	var wrapper string // empty until the first top-level dir is seen
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("backup: repack tar: %w", err)
		}
		trimmed := strings.TrimPrefix(strings.TrimPrefix(hdr.Name, "./"), "/")

		// Detect wrapper from the first entry: Docker CopyFromContainer
		// always emits the source dir as the very first entry, e.g. a
		// TypeDir whose name is "workspace/" or "workspace". After we
		// know the wrapper, strip it from every subsequent entry's name.
		if wrapper == "" && hdr.Typeflag == tar.TypeDir {
			noSlash := strings.TrimSuffix(trimmed, "/")
			if noSlash != "" && !strings.Contains(noSlash, "/") {
				wrapper = noSlash + "/"
				// Skip the wrapper entry itself — restore reconstructs
				// intermediate directories from descendant paths anyway.
				continue
			}
		}
		if wrapper != "" {
			trimmed = strings.TrimPrefix(trimmed, wrapper)
		}
		if trimmed == "" {
			continue
		}
		if shouldExclude(trimmed, excludes) {
			continue
		}
		newName := prefix + trimmed
		// Strip the wrapper from Linkname too — hardlink targets in
		// Docker's CopyFrom output reference paths like
		// "agent/.local/.../foo" (the same wrapper as entry names).
		// If we leave them prefixed, restore tries to hardlink to a
		// non-existent path and fails the whole extraction. Symlinks
		// are usually relative-to-entry-dir so this strip is a no-op
		// for them, but applies harmlessly.
		newLinkname := hdr.Linkname
		if wrapper != "" && newLinkname != "" {
			newLinkname = strings.TrimPrefix(newLinkname, wrapper)
		}
		if hdr.Typeflag != tar.TypeReg {
			// Non-regular entries (dirs, symlinks) pass through with an
			// empty body; rely on the outer writer for regular-file
			// framing but preserve metadata for dirs/symlinks too.
			if err := dst.tw.WriteHeader(&tar.Header{
				Name:     newName,
				Mode:     hdr.Mode,
				ModTime:  hdr.ModTime,
				Typeflag: hdr.Typeflag,
				Linkname: newLinkname,
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
