package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
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

	// CopyToPath extracts a tar stream into a destination INSIDE the
	// container by piping it through `tar -x` over an exec session,
	// under the identity and metadata policy the spec names.
	//
	// It replaced the pair CopyToVolume / CopyToSystem, which differed
	// only in the exec user. The reason it is now the ONLY extraction
	// path — including for /workspace and /crew, which Docker's native
	// CopyToContainer can reach — is #1714: CopyToContainer's
	// CopyUIDGID flag does not mean "use the archive's ownership". It
	// means "chown everything to the container's configured user", and
	// when Config.User is unset that resolves to the daemon's remapped
	// root. Ownership of restored data therefore depended on the daemon
	// build and on a field the backup package cannot see, which is why
	// the same restore produced 1001:1001 on one host and 0:0 on
	// another. An exec-tar under an explicit user produces the same
	// result on every runtime.
	//
	// tar must be on PATH inside the container; devcontainer base images
	// ship it. The container must be RUNNING — see RestoreCrew's
	// preflight, which says so before anything is written.
	CopyToPath(ctx context.Context, containerID string, spec ExtractSpec, content io.Reader) error

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

	// ExecAs is Exec under an explicit user. Restore needs it twice: to
	// probe destination writability as the identity that will do the
	// writing (a root probe would say yes where the agent gets EACCES),
	// and to re-apply the crew memory tree's group/setgid contract as
	// uid 1001 gid 1002 — the only identity that can, since crew
	// containers run CapDrop: ALL and even root there has no CAP_CHOWN.
	ExecAs(ctx context.Context, containerID, user string, cmd []string) (exitCode int, output []byte, err error)
}

// ExtractSpec is the per-section extraction policy handed to CopyToPath.
type ExtractSpec struct {
	// Dest is the container-absolute directory the tar is extracted into.
	Dest string

	// User is the exec identity, in "uid:gid" form. Files land owned by
	// it: tar runs with --no-same-owner because a cap-dropped container
	// cannot chown to anyone, so the exec identity IS the resulting
	// ownership. That is deliberate for disaster recovery — the source
	// instance's numeric ids need not mean anything on the target.
	User string

	// PreserveModes restores each entry's exact mode instead of applying
	// the destination's umask. Set for sections whose permission bits
	// are load-bearing (the crew memory tree's setgid dirs); left off
	// for the named volumes, where a mode change can EPERM on some
	// filesystem drivers and nothing depends on the exact bits.
	PreserveModes bool

	// PreserveTimes restores mtimes. Off for the named volumes, where
	// utime on the volume root returns EPERM; on for content whose
	// timestamps carry meaning (memory daily notes, workspace files).
	PreserveTimes bool

	// UnlinkFirst removes an existing entry before writing it, instead
	// of overwriting in place. The two are mutually exclusive in GNU tar
	// ("'--unlink-first' cannot be used with '--overwrite'"), so setting
	// this swaps one for the other.
	//
	// It exists for the crew memory section (#1746). Overwriting in
	// place makes tar chmod the existing file, and chmod is an OWNER
	// right — so replacing a file the AGENT wrote, while extracting as
	// the memory writer, fails with "Cannot change mode: Operation not
	// permitted" after the content is already written. Unlinking needs
	// write on the parent directory instead, which the memory group has
	// by design.
	//
	// Safe only for a section carrying no directory entries: on its own
	// --unlink-first tries to unlink directories too and fails on the
	// first non-empty one. The crew memory section filters them out.
	UnlinkFirst bool
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
	if _, err := m.Client.ContainerPause(ctx, containerID, client.ContainerPauseOptions{}); err != nil {
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
	if _, err := m.Client.ContainerUnpause(ctx, containerID, client.ContainerUnpauseOptions{}); err != nil {
		if strings.Contains(err.Error(), "is not paused") {
			return nil
		}
		return fmt.Errorf("backup: docker unpause %s: %w", containerID, err)
	}
	return nil
}

// CopyFrom implements DockerOps.
func (m *MobyDockerOps) CopyFrom(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error) {
	result, err := m.Client.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{SourcePath: srcPath})
	if err != nil {
		return nil, fmt.Errorf("backup: docker cp from %s:%s: %w", containerID, srcPath, err)
	}
	return result.Content, nil
}

// ContainerExists implements DockerOps by probing the daemon. Any
// error containing "No such container" resolves to (false, nil);
// other errors (daemon unreachable, permission denied) bubble up.
func (m *MobyDockerOps) ContainerExists(ctx context.Context, containerID string) (bool, error) {
	_, err := m.Client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
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
	if _, err := m.Client.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{
		DestinationPath:           dstPath,
		Content:                   content,
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
func (m *MobyDockerOps) CopyToPath(ctx context.Context, containerID string, spec ExtractSpec, content io.Reader) error {
	return m.copyToWithUser(ctx, containerID, spec, content)
}

func (m *MobyDockerOps) copyToWithUser(ctx context.Context, containerID string, spec ExtractSpec, content io.Reader) error {
	dstPath, user := spec.Dest, spec.User
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
	//   --same-permissions / --no-same-permissions and --touch are now
	//                       per-section (ExtractSpec). The volumes keep
	//                       the old defaults: modes off, because a mode
	//                       change can EPERM on some drivers, and mtimes
	//                       off, because utime on the volume root does.
	//                       The crew memory tree opts INTO both — its
	//                       setgid directory bits are the contract that
	//                       keeps crew-shared memory working, and a
	//                       memory note's mtime is content.
	//
	// Runs as the AGENT user (1001:1001) rather than root because the
	// volume's existing files were created by agent and root inside
	// the container often lacks write to user-owned files due to the
	// filesystem driver's uid remapping. Tar fails open with
	// "Permission denied" when root tries to overwrite an
	// agent-owned file under those conditions.
	// Not --unlink-first, and the reason is worth recording because it
	// looks like the obvious fix for a root-owned file the agent cannot
	// open for writing. GNU tar refuses to combine it with --overwrite
	// ("'--unlink-first' cannot be used with '--overwrite'", exit 2
	// before a byte is extracted), and on its own it tries to unlink
	// DIRECTORY entries too, which fails on the first non-empty one:
	// "tar: .config: Cannot unlink: Directory not empty". Both were
	// observed live rather than reasoned about. The only version that
	// works is --recursive-unlink, which deletes directories along with
	// their contents and would destroy whatever the restore excludes.
	//
	// So an entry the exec identity cannot replace is not made
	// replaceable here; it is caught by RestoreCrew's preflight, before
	// anything is written, and named to the operator.
	replace := "--overwrite"
	if spec.UnlinkFirst {
		replace = "--unlink-first"
	}
	cmd := []string{"tar", "-x", replace, "--no-same-owner"}
	if spec.PreserveModes {
		cmd = append(cmd, "--same-permissions")
	} else {
		cmd = append(cmd, "--no-same-permissions")
	}
	if !spec.PreserveTimes {
		cmd = append(cmd, "--touch")
	}
	cmd = append(cmd, "-f", "-", "-C", dstPath)
	exec, err := m.Client.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		User:         user,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("backup: exec-tar create %s:%s: %w", containerID, dstPath, err)
	}
	resp, err := m.Client.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
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
	//
	// If the source stream errors mid-copy we MUST close the hijacked
	// connection ourselves. Without it, tar inside the container keeps
	// waiting for stdin while CloseWrite is never called, the drain
	// goroutine never sees EOF, and the <-drainCh wait below blocks
	// forever — turning a corrupted bundle into a hung restore.
	pumpErr := func() error {
		if _, err := io.Copy(resp.Conn, content); err != nil {
			resp.Close()
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

	insp, err := m.Client.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
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
	return m.ExecAs(ctx, containerID, "0:0", cmd)
}

// ExecAs implements DockerOps.
func (m *MobyDockerOps) ExecAs(ctx context.Context, containerID, user string, cmd []string) (int, []byte, error) {
	exec, err := m.Client.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		User:         user,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return -1, nil, fmt.Errorf("backup: exec create %s: %w", containerID, err)
	}
	resp, err := m.Client.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
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
	inspect, err := m.Client.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
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
	".local/lib/",        // node_modules + python site-packages
	".local/share/mise/", // mise tool installations (re-fetchable)
	".local/share/cursor-agent/",
	".local/share/pnpm/",
	".local/share/yarn/",
	".local/state/", // logs/state we don't need
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

// shouldExclude reports whether a path inside a section should be
// skipped. Callers from CollectCrew pick the right exclusion list
// (volumeExclusions for the named volumes, varLibExclusions for
// /var/lib, nil for /workspace and /output where every byte is
// user-meaningful). Path is always wrapper-stripped (e.g.
// ".cache/mise/foo") not raw.
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
// Returns what was written to dst — see RepackResult.
func RepackTar(src io.Reader, dst *TarZstWriter, prefix string) (RepackResult, error) {
	return RepackTarWithExcludes(src, dst, prefix, volumeExclusions)
}

// RepackResult reports what a repack produced.
//
// It exists because the manifest used to assert what a bundle held
// instead of observing it (#1713). A bundle that lies about its contents
// is worse than one that is missing them: the operator stops looking.
//
// The three counts are not interchangeable, and picking the wrong one
// reintroduces the lie in a new shape:
//
//   - Entries counts every tar header, directories included. Useful for
//     "did this section produce anything at all"; useless as evidence of
//     content, because the docker provider's prepareCrewDirs creates
//     crews/<id>/shared and crews/<id>/agents at container-creation
//     time. A crew that has never written a single memory note still
//     yields Entries >= 2.
//   - Files counts non-directory entries — real content the restore has
//     something to put back.
//   - MemoryFiles counts regular files that live inside a `.memory`
//     directory. Only meaningful for the /crew section, and it is what
//     `memory_included` is set from: the flag's name promises the
//     agent's memory is in the bundle, and nothing weaker than "there
//     are files in a .memory directory" makes that true.
type RepackResult struct {
	Entries     int
	Files       int
	MemoryFiles int
	Bytes       int64
}

// isInsideMemoryDir reports whether a section-relative path lies inside a
// `.memory` directory — `agents/alex/.memory/AGENT.md` and
// `shared/.memory/topics/x/pins.md` both do, `init.sh` does not.
//
// Matching on a path COMPONENT rather than a substring: a file named
// `notes.memory.md` is not memory, and a directory called `.memoryX`
// is not the memory tree.
func isInsideMemoryDir(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if seg == ".memory" {
			return true
		}
	}
	return false
}

// RepackTarWithExcludes is RepackTar with an explicit exclusion list,
// so /var/lib (where dpkg/apt state should be skipped, NOT
// node_modules) and /workspace (the opposite) can both be repacked
// through one code path.
func RepackTarWithExcludes(src io.Reader, dst *TarZstWriter, prefix string, excludes []string) (RepackResult, error) {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	tr := tar.NewReader(src)
	var res RepackResult
	var wrapper string // empty until the first top-level dir is seen
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("backup: repack tar: %w", err)
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
				Uname:    hdr.Uname,
				Gname:    hdr.Gname,
			}); err != nil {
				return res, fmt.Errorf("backup: repack header %q: %w", newName, err)
			}
			res.Entries++
			// Symlinks and hardlinks are content; directories are not.
			// A tree of empty directories has nothing for a restore to
			// put back, and counting them is what let a crew's
			// provider-created skeleton read as "memory included".
			if hdr.Typeflag != tar.TypeDir {
				res.Files++
				if isInsideMemoryDir(trimmed) {
					res.MemoryFiles++
				}
			}
			continue
		}
		// Regular files carry Uid/Gid too. Before #1714 they did not, and
		// the zero value is uid 0 — so every file in a bundle claimed to
		// belong to root while the directories around it correctly
		// claimed uid 1001. Whether a given restore path honours the
		// header is a separate question (see ExtractSpec); a bundle that
		// records the wrong owner cannot be restored correctly by any of
		// them.
		if err := dst.WriteStreamOwned(newName, hdr.Mode, hdr.ModTime, hdr.Size, hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname, tr); err != nil {
			return res, err
		}
		res.Entries++
		res.Files++
		if isInsideMemoryDir(trimmed) {
			res.MemoryFiles++
		}
		res.Bytes += hdr.Size
	}
	return res, nil
}
