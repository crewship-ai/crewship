//go:build linux

package appengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
	// engineGroups holds the process groups of engine executions that are
	// still running, so a concurrent sweep does not mistake them for escapes.
	engineGroups = struct {
		sync.Mutex
		live map[int]struct{}
	}{live: map[int]struct{}{}}
)

// PrepareEngineSandbox verifies the kernel can confine the engine and makes
// this process the reaper of every engine descendant. The worker calls it at
// startup and refuses to serve if it fails; no unconfined fallback exists.
func PrepareEngineSandbox() error {
	if _, err := landlockABI(); err != nil {
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
	argv := []string{EngineSandboxFlag, "--rw", dir, "--ro", e.Binary}
	for _, root := range append(append([]string(nil), defaultEngineReadOnly...), e.ReadOnly...) {
		argv = append(argv, "--ro", root)
	}
	argv = append(append(argv, "--", e.Binary), args...)
	cmd := exec.CommandContext(ctx, launcher, argv...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	// The CLI prints file paths. Keep them inside the worker; protocol errors do
	// not copy process output into API responses or private Artifact logs. Nil
	// streams are /dev/null, so no pipe can keep Wait open after the group dies.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second

	engineGroups.Lock()
	err := cmd.Start()
	if err == nil {
		engineGroups.live[cmd.Process.Pid] = struct{}{}
	}
	engineGroups.Unlock()
	if err != nil {
		return fmt.Errorf("workbook engine execution failed: %w", err)
	}
	group := cmd.Process.Pid
	// Wait for the leader to exit without reaping it: its PID, and so the
	// group ID, cannot be reused until the whole group has been killed.
	waitErr := waitExitedNoReap(group)
	_ = syscall.Kill(-group, syscall.SIGKILL)
	runErr := cmd.Wait()
	engineGroups.Lock()
	delete(engineGroups.live, group)
	engineGroups.Unlock()
	if err := sweepEngineSurvivors(group); err != nil {
		if e.Escaped != nil {
			e.Escaped()
		}
		return err
	}
	if waitErr != nil {
		return fmt.Errorf("%w: engine wait failed", ErrEngineUnavailable)
	}
	if runErr != nil {
		return fmt.Errorf("workbook engine execution failed: %w", runErr)
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
	pid, pgid int
	zombie    bool
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
		if len(fields) < 3 {
			return nil, fmt.Errorf("malformed /proc/%d/stat", pid)
		}
		ppid, err1 := strconv.Atoi(string(fields[1]))
		pgid, err2 := strconv.Atoi(string(fields[2]))
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("malformed /proc/%d/stat", pid)
		}
		if ppid == self {
			children = append(children, procEntry{pid: pid, pgid: pgid, zombie: fields[0][0] == 'Z' || fields[0][0] == 'X'})
		}
	}
	return children, nil
}

// sweepEngineSurvivors runs after the engine group was killed. Members of that
// group are dying and are reaped. Any other child that does not belong to a
// running engine left the group (setsid, setpgid): it is killed and reported
// as an escape. Descendants reparented while killing are swept in later rounds.
func sweepEngineSurvivors(group int) error {
	ownGroup := syscall.Getpgrp()
	deadline := time.Now().Add(2 * time.Second)
	escaped := false
	for {
		children, err := workerChildren()
		if err != nil {
			return fmt.Errorf("%w: cannot verify engine processes", ErrEngineEscaped)
		}
		engineGroups.Lock()
		pending := false
		for _, child := range children {
			if _, running := engineGroups.live[child.pgid]; running && child.pgid != group {
				continue
			}
			if child.pgid != group {
				escaped = true
				_ = syscall.Kill(child.pid, syscall.SIGKILL)
				if child.pgid == child.pid && child.pgid != ownGroup {
					_ = syscall.Kill(-child.pgid, syscall.SIGKILL)
				}
			}
			if child.zombie {
				var status unix.WaitStatus
				_, _ = unix.Wait4(child.pid, &status, unix.WNOHANG, nil)
			}
			pending = true
		}
		engineGroups.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			escaped = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if escaped {
		return ErrEngineEscaped
	}
	return nil
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
		default:
			return errors.New("invalid engine sandbox arguments")
		}
		args = args[2:]
	}
	if rw == "" || len(args) < 2 {
		return errors.New("invalid engine sandbox arguments")
	}
	binary, argv := args[1], args[1:]
	// Landlock and no_new_privs apply to this thread; execve from the same
	// thread carries them into the engine.
	runtime.LockOSThread()
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
	return unix.Exec(binary, argv, os.Environ())
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
