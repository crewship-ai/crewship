//go:build linux

package appengine

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// engineNamespaceFlags are the clone/unshare flags that create namespaces.
const engineNamespaceFlags = unix.CLONE_NEWNS | unix.CLONE_NEWCGROUP | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC |
	unix.CLONE_NEWUSER | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWTIME

// seccompArch is the audit architecture of this build. Only these builds can
// confine the engine; any other fails closed.
func seccompArch() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	}
	return 0, fmt.Errorf("engine seccomp filter unsupported on %s", runtime.GOARCH)
}

// seccompAvailable verifies the kernel accepts the filter's actions. It does
// not install anything in the worker.
func seccompAvailable() error {
	if _, err := seccompArch(); err != nil {
		return err
	}
	for _, action := range []uint32{unix.SECCOMP_RET_ALLOW, unix.SECCOMP_RET_ERRNO} {
		a := action
		if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_GET_ACTION_AVAIL, 0, uintptr(unsafe.Pointer(&a))); errno != 0 {
			return fmt.Errorf("seccomp unavailable: %w", errno)
		}
	}
	return nil
}

// Offsets in struct seccomp_data; arguments are read as their low 32 bits,
// which hold every flag checked here (both supported builds are little-endian).
const (
	seccompNR   = 0
	seccompArg0 = 16
	seccompAch  = 4
)

func bpfStmt(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }
func bpfJump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// engineSeccompProgram refuses with EPERM the calls that would let an engine
// process leave its candidate's session or hide descendants from the worker:
// setsid, prctl(PR_SET_CHILD_SUBREAPER), unshare, and clone with namespace
// flags. clone3 passes its flags in memory a filter cannot inspect, so it
// gets ENOSYS and libc falls back to clone. Foreign-architecture and x32
// system calls are refused. Everything else is allowed.
func engineSeccompProgram(arch uint32) []unix.SockFilter {
	const (
		ld   = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		jeq  = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
		jge  = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
		jset = unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K
		ret  = unix.BPF_RET | unix.BPF_K
	)
	allow := uint32(unix.SECCOMP_RET_ALLOW)
	eperm := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)
	enosys := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.ENOSYS)
	// Layout; jump offsets are relative to the next instruction.
	//  0 ld arch
	//  1 jeq arch ? 2 : deny(16)
	//  2 ld nr
	//  3 jge x32 bit ? deny(16) : 4
	//  4 jeq setsid ? deny : 5
	//  5 jeq unshare ? deny : 6
	//  6 jeq clone3 ? enosys(17) : 7
	//  7 jeq clone ? 10 : 8
	//  8 jeq prctl ? 13 : 9
	//  9 ret allow
	// 10 ld arg0
	// 11 jset ns flags ? deny : 12
	// 12 ret allow
	// 13 ld arg0
	// 14 jeq PR_SET_CHILD_SUBREAPER ? deny : 15
	// 15 ret allow
	// 16 ret EPERM
	// 17 ret ENOSYS
	return []unix.SockFilter{
		bpfStmt(ld, seccompAch),
		bpfJump(jeq, arch, 0, 14),
		bpfStmt(ld, seccompNR),
		bpfJump(jge, 0x40000000, 12, 0),
		bpfJump(jeq, uint32(unix.SYS_SETSID), 11, 0),
		bpfJump(jeq, uint32(unix.SYS_UNSHARE), 10, 0),
		bpfJump(jeq, uint32(unix.SYS_CLONE3), 10, 0),
		bpfJump(jeq, uint32(unix.SYS_CLONE), 2, 0),
		bpfJump(jeq, uint32(unix.SYS_PRCTL), 4, 0),
		bpfStmt(ret, allow),
		bpfStmt(ld, seccompArg0),
		bpfJump(jset, uint32(engineNamespaceFlags), 4, 0),
		bpfStmt(ret, allow),
		bpfStmt(ld, seccompArg0),
		bpfJump(jeq, unix.PR_SET_CHILD_SUBREAPER, 1, 0),
		bpfStmt(ret, allow),
		bpfStmt(ret, eperm),
		bpfStmt(ret, enosys),
	}
}

// installEngineSeccomp confines the calling thread; execve from the same
// thread carries the filter into the engine and all its descendants. The
// caller must already have set no_new_privs.
func installEngineSeccomp() error {
	arch, err := seccompArch()
	if err != nil {
		return err
	}
	filter := engineSeccompProgram(arch)
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		return fmt.Errorf("seccomp filter: %w", errno)
	}
	runtime.KeepAlive(filter)
	return nil
}
