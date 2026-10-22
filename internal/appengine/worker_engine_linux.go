//go:build linux

package appengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ErrEngineEscaped means a process started by the engine outlived its
// candidate. The worker cannot prove its state any more and must restart.
var ErrEngineEscaped = fmt.Errorf("%w: engine process outlived its candidate", ErrEngineUnavailable)

// defaultEngineReadOnly is everything the dynamically linked pinned CLI needs
// to load: the binary, glibc, libm and libgcc_s. Missing paths are skipped.
var defaultEngineReadOnly = []string{"/usr", "/lib", "/lib64", "/etc/ld.so.cache"}

// Device nodes the engine may use; nothing else under /dev is reachable.
var engineDevices = map[string]uint64{
	"/dev/null":    unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE,
	"/dev/urandom": unix.LANDLOCK_ACCESS_FS_READ_FILE,
	"/dev/random":  unix.LANDLOCK_ACCESS_FS_READ_FILE,
}

var (
	subreaperOnce sync.Once
	subreaperErr  error
	// engineRuns holds every engine execution from start until its sweep is
	// complete, keyed by its session ID. Each execution runs in its own session,
	// so a concurrent sweep can attribute any descendant that kept the session,
	// and never mistakes another execution's dying processes for an escape.
	// The lock also covers starting executions and reading /proc, so a sweep
	// never sees a child between fork and setsid.
	engineRuns = struct {
		sync.Mutex
		live map[int]*engineRun
	}{live: map[int]*engineRun{}}
)

type engineRun struct {
	session int
	// escaped is set when a process of this execution left its process group,
	// or when a process no execution can be attributed to appeared while this
	// execution was in flight.
	escaped bool
}

// PrepareEngineSandbox verifies the kernel can confine the engine and makes
// this process the reaper of every engine descendant. The worker calls it at
// startup and refuses to serve if it fails; no unconfined fallback exists.
func PrepareEngineSandbox() error {
	if _, err := landlockABI(); err != nil {
		return err
	}
	if err := seccompAvailable(); err != nil {
		return err
	}
	subreaperOnce.Do(func() {
		// Orphaned engine descendants are reparented to this process (PID 1 in
		// the image does this anyway), so the post-candidate sweep sees them.
		subreaperErr = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
	})
	return subreaperErr
}

func (e WorkerEngine) execEngine(ctx context.Context, dir string, args []string) error {
	if err := PrepareEngineSandbox(); err != nil {
		return fmt.Errorf("%w: engine sandbox unavailable", ErrEngineUnavailable)
	}
	launcher := e.Launcher
	if launcher == "" {
		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("%w: engine launcher unavailable", ErrEngineUnavailable)
		}
		launcher = self
	}
	limits := e.Limits.resolved()
	argv := []string{EngineSandboxFlag, "--rw", dir, "--ro", e.Binary,
		"--as", strconv.FormatUint(limits.AddressSpace, 10),
		"--fsize", strconv.FormatUint(limits.FileSize, 10),
		"--cpu", strconv.FormatUint(limits.CPUSeconds, 10)}
	for _, root := range append(append([]string(nil), defaultEngineReadOnly...), e.ReadOnly...) {
		argv = append(argv, "--ro", root)
	}
	argv = append(append(argv, "--", e.Binary), args...)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workbook engine execution failed: %w", err)
	}
	cmd := exec.Command(launcher, argv...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	// The CLI prints file paths. Keep them inside the worker; protocol errors do
	// not copy process output into API responses or private Artifact logs. Nil
	// streams are /dev/null, so no pipe can keep Wait open after the group dies.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	// A new session (and so a new process group with the same ID) per
	// execution: setpgid can move a descendant only within its session, so a
	// descendant either keeps this session ID or calls setsid.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}

	run := &engineRun{}
	engineRuns.Lock()
	err := cmd.Start()
	if err == nil {
		run.session = cmd.Process.Pid
		engineRuns.live[run.session] = run
	}
	engineRuns.Unlock()
	if err != nil {
		return fmt.Errorf("workbook engine execution failed: %w", err)
	}
	group := run.session
	// Cancellation kills the group only while the leader has not exited. The
	// leader stays unreaped until the sweep ends, so the ID cannot be reused.
	var leader sync.Mutex
	exited := false
	stopCancel := context.AfterFunc(ctx, func() {
		leader.Lock()
		defer leader.Unlock()
		if !exited {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	})
	// Wait for the leader to exit without reaping it: its PID, and so the
	// session and group ID, cannot be reused until the sweep is complete.
	waitErr := waitExitedNoReap(group)
	leader.Lock()
	exited = true
	leader.Unlock()
	stopCancel()
	_ = syscall.Kill(-group, syscall.SIGKILL)
	sweepErr := sweepEngineSurvivors(run)
	// Reap the leader and deregister atomically: no other sweep can see the
	// reaped session unregistered, and no new execution can reuse its ID while
	// it is still registered.
	engineRuns.Lock()
	runErr := cmd.Wait()
	delete(engineRuns.live, group)
	escaped := run.escaped
	engineRuns.Unlock()
	if sweepErr != nil || escaped {
		if e.Escaped != nil {
			e.Escaped()
		}
		if sweepErr != nil {
			return sweepErr
		}
		return ErrEngineEscaped
	}
	if waitErr != nil {
		return fmt.Errorf("%w: engine wait failed", ErrEngineUnavailable)
	}
	if runErr != nil {
		return fmt.Errorf("workbook engine execution failed: %w", runErr)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("workbook engine execution failed: %w", err)
	}
	return nil
}

func waitExitedNoReap(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

type procEntry struct {
	pid, pgid, session int
	zombie             bool
}

// workerChildren lists this process's children from /proc. Because the worker
// is the subreaper, every surviving engine descendant either is such a child
// or has an ancestor that is.
func workerChildren() ([]procEntry, error) {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var children []procEntry
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue // exited and reaped meanwhile
		}
		end := bytes.LastIndexByte(stat, ')')
		if end < 0 || end+2 > len(stat) {
			return nil, fmt.Errorf("malformed /proc/%d/stat", pid)
		}
		fields := bytes.Fields(stat[end+2:])
		if len(fields) < 4 {
			return nil, fmt.Errorf("malformed /proc/%d/stat", pid)
		}
		ppid, err1 := strconv.Atoi(string(fields[1]))
		pgid, err2 := strconv.Atoi(string(fields[2]))
		session, err3 := strconv.Atoi(string(fields[3]))
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("malformed /proc/%d/stat", pid)
		}
		if ppid == self {
			children = append(children, procEntry{pid: pid, pgid: pgid, session: session, zombie: fields[0][0] == 'Z' || fields[0][0] == 'X'})
		}
	}
	return children, nil
}

// sweepEngineSurvivors runs after the execution's group was killed, before
// its leader is reaped. Every child of the worker is classified:
//   - in another registered session: that execution's own sweep handles it;
//   - in this session and group: dying, reaped here;
//   - in this session but another group (setpgid): an escape of this
//     execution only, killed;
//   - in no registered session (setsid): not attributable, killed, and every
//     execution in flight is marked escaped, so none returns output.
//
// Descendants reparented while killing are swept in later rounds. If this
// execution's processes remain after the deadline it counts as an escape.
func sweepEngineSurvivors(run *engineRun) error {
	ownGroup := syscall.Getpgrp()
	deadline := time.Now().Add(2 * time.Second)
	for {
		engineRuns.Lock()
		// The unreaped leader holds the group ID, so this cannot hit another
		// group; it also kills a process that rejoined the group after the
		// first kill.
		_ = syscall.Kill(-run.session, syscall.SIGKILL)
		children, err := workerChildren()
		if err != nil {
			run.escaped = true
			engineRuns.Unlock()
			return fmt.Errorf("%w: cannot verify engine processes", ErrEngineEscaped)
		}
		pending := false
		for _, child := range children {
			if child.pid == run.session {
				continue // the unreaped leader; reaped after the sweep
			}
			if other, registered := engineRuns.live[child.session]; registered && other != run {
				continue
			}
			if child.session == run.session {
				if child.pgid != run.session {
					run.escaped = true
					killEscapee(child, ownGroup)
				}
			} else {
				for _, inFlight := range engineRuns.live {
					inFlight.escaped = true
				}
				killEscapee(child, ownGroup)
			}
			if child.zombie {
				var status unix.WaitStatus
				_, _ = unix.Wait4(child.pid, &status, unix.WNOHANG, nil)
			}
			pending = true
		}
		engineRuns.Unlock()
		if !pending {
			return nil
		}
		if time.Now().After(deadline) {
			engineRuns.Lock()
			run.escaped = true
			engineRuns.Unlock()
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// killEscapee kills a process outside its execution's group, and its own
// group when it leads one that is neither the worker's nor an execution's.
// The caller holds engineRuns.
func killEscapee(child procEntry, ownGroup int) {
	_ = syscall.Kill(child.pid, syscall.SIGKILL)
	if _, registered := engineRuns.live[child.pgid]; child.pgid == child.pid && child.pgid != ownGroup && !registered {
		_ = syscall.Kill(-child.pgid, syscall.SIGKILL)
	}
}

// engineIdentity is what a change of the CLI file necessarily changes: a
// replacement changes the device or inode, a write changes size, mtime and
// ctime, and ctime cannot be set back by an unprivileged process.
type engineIdentity struct {
	dev, ino, size, mode, uid uint64
	mtime, ctime              unix.Timespec
}

func identityOf(st *unix.Stat_t) engineIdentity {
	return engineIdentity{dev: st.Dev, ino: st.Ino, size: uint64(st.Size), mode: uint64(st.Mode), uid: uint64(st.Uid), mtime: st.Mtim, ctime: st.Ctim}
}

func fileIdentity(f *os.File) (engineIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return engineIdentity{}, err
	}
	return identityOf(&st), nil
}

// check confirms the path still names the verified file. It follows links as
// exec does. A mismatch makes the engine unavailable: the worker is
// quarantined and only a restart, which verifies the digest again, recovers.
func (id *engineIdentity) check(path string) error {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil || identityOf(&st) != *id {
		return fmt.Errorf("%w: engine binary changed since it was verified", ErrEngineUnavailable)
	}
	return nil
}

// readBounded reads one engine output file. It never follows a link and
// accepts only a regular file with a single name, so even a weakened sandbox
// cannot make the output a view of a worker-private file.
func readBounded(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: engine output unavailable", ErrCandidate)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return nil, fmt.Errorf("%w: engine output is not a private regular file", ErrCandidate)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSnapshotBytes {
		return nil, ErrInvalid
	}
	return b, nil
}

// landlockRuleset is the kernel ABI v6 prefix of struct landlock_ruleset_attr.
// Only the prefix the running kernel knows is passed.
type landlockRuleset struct {
	handledFS, handledNet, scoped uint64
}

func landlockABI() (int, error) {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, fmt.Errorf("landlock unavailable: %w", errno)
	}
	if abi < 1 {
		return 0, errors.New("landlock unavailable")
	}
	return int(abi), nil
}

const landlockFileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV

// ExecEngineSandbox implements EngineSandboxFlag. It confines the calling
// thread with Landlock and replaces the process with the engine. Arguments:
// --rw DIR, any number of --ro PATH, then -- BINARY ARGS...
// The engine can read and execute only the read-only roots and use only its
// private candidate directory: not the service secret, the socket directory,
// other candidates' scratch, /proc or the network. It returns only on failure.
func ExecEngineSandbox(args []string) error {
	var rw string
	var ro []string
	var limits EngineLimits
	for len(args) > 0 && args[0] != "--" {
		if len(args) < 2 {
			return errors.New("invalid engine sandbox arguments")
		}
		switch args[0] {
		case "--rw":
			if rw != "" {
				return errors.New("invalid engine sandbox arguments")
			}
			rw = args[1]
		case "--ro":
			ro = append(ro, args[1])
		case "--as", "--fsize", "--cpu":
			n, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil || n == 0 {
				return errors.New("invalid engine sandbox arguments")
			}
			switch args[0] {
			case "--as":
				limits.AddressSpace = n
			case "--fsize":
				limits.FileSize = n
			default:
				limits.CPUSeconds = n
			}
		default:
			return errors.New("invalid engine sandbox arguments")
		}
		args = args[2:]
	}
	if rw == "" || len(args) < 2 || limits.AddressSpace == 0 || limits.FileSize == 0 || limits.CPUSeconds == 0 {
		return errors.New("invalid engine sandbox arguments")
	}
	binary, argv := args[1], args[1:]
	// Landlock and no_new_privs apply to this thread; execve from the same
	// thread carries them into the engine.
	runtime.LockOSThread()
	// Make the engine the first OOM victim, ahead of the worker and other
	// candidates. Raising the score needs no privilege; /proc is still
	// reachable because Landlock is not applied yet.
	if err := os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0); err != nil {
		return fmt.Errorf("oom_score_adj: %w", err)
	}
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	attr := landlockRuleset{handledFS: unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM}
	size := unsafe.Sizeof(attr.handledFS)
	if abi >= 2 {
		attr.handledFS |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		attr.handledFS |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 4 {
		attr.handledNet = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
		size = unsafe.Offsetof(attr.scoped)
	}
	if abi >= 5 {
		attr.handledFS |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	if abi >= 6 {
		// Also stops the engine signalling the worker or reaching abstract sockets.
		attr.scoped = unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL
		size = unsafe.Sizeof(attr)
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0)
	if errno != 0 {
		return fmt.Errorf("landlock ruleset: %w", errno)
	}
	ruleset := int(fd)
	defer unix.Close(ruleset)
	readOnly := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_EXECUTE)
	for _, path := range ro {
		if err := landlockAllow(ruleset, path, readOnly&attr.handledFS, true); err != nil {
			return err
		}
	}
	for path, access := range engineDevices {
		if err := landlockAllow(ruleset, path, access&attr.handledFS, true); err != nil {
			return err
		}
	}
	scratch := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	if err := landlockAllow(ruleset, rw, scratch&attr.handledFS, false); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("landlock restrict: %w", errno)
	}
	if err := unix.Close(ruleset); err != nil {
		return err
	}
	if err := installEngineSeccomp(); err != nil {
		return err
	}
	return execWithLimits(binary, argv, os.Environ(), limits)
}

// execWithLimits applies the engine's resource limits and replaces the
// process with the engine. The limits come last so the Go launcher never runs
// under the engine's address space limit; between them and execve nothing
// allocates and the garbage collector is off, so the runtime does not need
// new mappings in that window.
func execWithLimits(binary string, argv, env []string, limits EngineLimits) error {
	path, err := syscall.BytePtrFromString(binary)
	if err != nil {
		return err
	}
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return err
	}
	envp, err := syscall.SlicePtrFromStrings(env)
	if err != nil {
		return err
	}
	resources := [...]struct {
		resource int
		limit    unix.Rlimit
	}{
		{unix.RLIMIT_AS, unix.Rlimit{Cur: limits.AddressSpace, Max: limits.AddressSpace}},
		{unix.RLIMIT_FSIZE, unix.Rlimit{Cur: limits.FileSize, Max: limits.FileSize}},
		{unix.RLIMIT_CPU, unix.Rlimit{Cur: limits.CPUSeconds, Max: limits.CPUSeconds}},
		{unix.RLIMIT_CORE, unix.Rlimit{}},
	}
	debug.SetGCPercent(-1)
	for i := range resources {
		if _, _, errno := unix.RawSyscall6(unix.SYS_PRLIMIT64, 0, uintptr(resources[i].resource),
			uintptr(unsafe.Pointer(&resources[i].limit)), 0, 0, 0); errno != 0 {
			return fmt.Errorf("engine resource limit: %w", errno)
		}
	}
	_, _, errno := unix.RawSyscall(unix.SYS_EXECVE, uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(&argvp[0])), uintptr(unsafe.Pointer(&envp[0])))
	runtime.KeepAlive(argvp)
	runtime.KeepAlive(envp)
	return fmt.Errorf("engine exec: %w", errno)
}

func landlockAllow(ruleset int, path string, access uint64, optional bool) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if optional && errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("landlock path %s: %w", path, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= landlockFileRights
	}
	if access == 0 {
		return nil
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(fd)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock rule %s: %w", path, errno)
	}
	return nil
}

// makeRemovable gives the worker back write and search permission on every
// directory under dir, which the engine owns and may have chmod-ed (Landlock
// does not restrict chmod). It walks by file descriptor with O_NOFOLLOW, so a
// link never redirects it outside the candidate directory.
func makeRemovable(dir string) error {
	// The worker created dir; the engine cannot replace it (it has no write
	// access to the scratch root), only change its mode.
	var st unix.Stat_t
	if err := unix.Lstat(dir, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("candidate directory is not a directory")
	}
	if err := unix.Chmod(dir, 0700); err != nil {
		return err
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	return makeRemovableAt(fd, 0)
}

func makeRemovableAt(fd, depth int) error {
	f := os.NewFile(uintptr(fd), "candidate")
	defer f.Close()
	if depth > 64 {
		return errors.New("candidate directory too deep")
	}
	if err := unix.Fchmod(fd, 0700); err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // links and files: removing them needs only the parent
		}
		child, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.EACCES) {
			if err = unix.Fchmodat(fd, entry.Name(), 0700, 0); err != nil {
				return err
			}
			child, err = unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err != nil {
			return err
		}
		if err := makeRemovableAt(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
